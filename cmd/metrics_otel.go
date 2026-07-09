// ----------------------------------------------------------------------------
// Custom OpenTelemetry instruments for the go-rest-api SLIs that are not
// covered by the otelchi auto-instrumentation:
//   - Request outcome counter (availability)
//   - Active-request / worker-pool saturation gauges
//   - Auth attempt outcome counter (auth failure rate)
//   - Per-tenant / per-route request-rate counter (throughput)
//   - Flow outcome / entry counters and duration histogram (business flow SLIs)
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterScope = "github.com/benc-uk/go-rest-api"

var (
	meter metric.Meter

	// requestOutcomeCounter counts every completed request labeled by route and
	// outcome class (success / client_error / server_error) — availability SLI.
	requestOutcomeCounter metric.Int64Counter

	// authAttemptsCounter counts auth attempts labeled by outcome (allowed/denied)
	// and reason for denial — auth failure rate SLI.
	authAttemptsCounter metric.Int64Counter

	// tenantRequestCounter counts requests per tenant/api-key and route —
	// per-tenant throughput SLI.
	tenantRequestCounter metric.Int64Counter

	// flowOutcomeCounter counts terminal outcomes of the primary business flow.
	flowOutcomeCounter metric.Int64Counter

	// flowEntryCounter counts every entry into the primary business flow,
	// independent of eventual outcome — flow throughput SLI.
	flowEntryCounter metric.Int64Counter

	// flowDurationHistogram records end-to-end flow duration in seconds —
	// flow latency / freshness SLIs.
	flowDurationHistogram metric.Float64Histogram

	// validationOutcomeCounter counts validation step outcomes — flow validation
	// failure rate SLI.
	validationOutcomeCounter metric.Int64Counter

	// activeRequests tracks in-flight HTTP requests for saturation gauges.
	activeRequests atomic.Int64

	// maxWorkers is the configured worker pool size (best-effort static value
	// for this simple server; adjust if a real pool is introduced).
	maxWorkers int64 = 100
)

// initMetrics obtains the meter and creates all custom instruments. It MUST
// be called after otel.SetMeterProvider has registered the real provider
// (see setupTelemetryMetrics in otel.go, invoked from main()) — otherwise the
// instruments bind to the no-op default provider and export nothing.
func initMetrics() {
	meter = otel.Meter(meterScope)

	var err error

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests labeled by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create requestOutcomeCounter: %v", err)
	}

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization attempts labeled by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create authAttemptsCounter: %v", err)
	}

	tenantRequestCounter, err = meter.Int64Counter(
		"http.server.requests.by_tenant",
		metric.WithDescription("Count of HTTP requests labeled by tenant/api-key and route"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create tenantRequestCounter: %v", err)
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of primary business flow terminal outcomes"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create flowOutcomeCounter: %v", err)
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of entries into the primary business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create flowEntryCounter: %v", err)
	}

	flowDurationHistogram, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create flowDurationHistogram: %v", err)
	}

	validationOutcomeCounter, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Count of request validation step outcomes"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create validationOutcomeCounter: %v", err)
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create activeRequestsGauge: %v", err)
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured worker pool size for the HTTP server"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create poolSizeGauge: %v", err)
	}

	if activeRequestsGauge != nil && poolSizeGauge != nil {
		_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(activeRequestsGauge, activeRequests.Load())
			o.ObserveInt64(poolSizeGauge, maxWorkers)
			return nil
		}, activeRequestsGauge, poolSizeGauge)
		if err != nil {
			log.Printf("### ⚠️  Failed to register saturation gauge callback: %v", err)
		}
	}
}

// outcomeClass buckets an HTTP status code into a low-cardinality class.
func outcomeClass(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}

// chiRouteCtx extracts the matched chi route pattern (template) for the
// request, e.g. "/things/{id}", falling back to "" if unavailable so callers
// can fall back to the raw path.
func chiRouteCtx(req *http.Request) string {
	rctx := chi.RouteContext(req.Context())
	if rctx == nil {
		return ""
	}
	return rctx.RoutePattern()
}

// activeRequestsMiddleware tracks in-flight requests (for saturation gauges),
// records the request outcome counter, and increments the flow entry/outcome
// counters and per-tenant request counter, without altering response behavior.
func activeRequestsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		activeRequests.Add(1)
		defer activeRequests.Add(-1)

		// Flow-entry counter: every inbound request is an entry into the primary flow.
		flowEntryCounter.Add(req.Context(), 1)

		tenant := req.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}

		sw := middleware.NewWrapResponseWriter(resp, req.ProtoMajor)
		next.ServeHTTP(sw, req)

		status := sw.Status()
		if status == 0 {
			status = http.StatusOK
		}
		class := outcomeClass(status)

		routeTemplate := req.URL.Path
		if rc := chiRouteCtx(req); rc != "" {
			routeTemplate = rc
		}

		requestOutcomeCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", routeTemplate),
			attribute.String("outcome", class),
		))

		tenantRequestCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", routeTemplate),
			attribute.String("tenant", tenant),
		))

		flowOutcome := "success"
		if class != "success" {
			flowOutcome = "failure"
		}
		flowOutcomeCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", flowOutcome),
		))
	})
}
