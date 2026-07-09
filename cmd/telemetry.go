// ----------------------------------------------------------------------------
// Custom telemetry: request outcome counter, auth-attempt outcome counter,
// worker-pool saturation gauges, and helpers for error.type span attributes.
// All instruments are created from a single package-level meter.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Single package-level meter for the whole service.
var meter = otel.Meter("github.com/benc-uk/go-rest-api")

var (
	requestOutcomeCounter metric.Int64Counter
	authAttemptsCounter   metric.Int64Counter
	flowEntryCounter      metric.Int64Counter
	flowOutcomeCounter    metric.Int64Counter

	activeRequests int64 // in-flight request count, tracked via UpDownCounter semantics through an observable gauge
)

func init() {
	var err error

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(err)
	}

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		panic(err)
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of primary business flow entry invocations"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		panic(err)
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of primary business flow terminal outcomes"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		panic(err)
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(err)
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured worker pool size (GOMAXPROCS) for the HTTP server"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		panic(err)
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
		o.ObserveInt64(poolSizeGauge, int64(runtime.GOMAXPROCS(0)))
		return nil
	}, activeRequestsGauge, poolSizeGauge)
	if err != nil {
		panic(err)
	}
}

// errorTypeAttr builds the standard error.type span attribute.
func errorTypeAttr(errType string) attribute.KeyValue {
	return attribute.String("error.type", errType)
}

// statusRecorder wraps http.ResponseWriter to capture the status code while
// forwarding all optional interfaces the underlying writer may implement.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requestOutcomeMiddleware emits the request outcome counter (by route and
// outcome class) and tracks in-flight request count for saturation gauges,
// and sets error.type on the span for 5xx outcomes.
func requestOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequests, 1)
		defer atomic.AddInt64(&activeRequests, -1)

		rec := &statusRecorder{ResponseWriter: w, status: 0}

		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}

		outcome := "success"
		if status >= 500 {
			outcome = "error"
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(errorTypeAttr("HTTP5xx"))
		} else if status >= 400 {
			outcome = "client_error"
		}

		routeAttr := attribute.String("http.route", routeTemplate(r))
		statusAttr := attribute.Int("http.response.status_code", status)
		outcomeAttr := attribute.String("outcome", outcome)

		requestOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(routeAttr, statusAttr, outcomeAttr))

		// Business flow rollup: every request is a flow entry; terminal outcome
		// mirrors the HTTP outcome class.
		flowEntryCounter.Add(r.Context(), 1, metric.WithAttributes(routeAttr))
		flowOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(routeAttr, outcomeAttr))
	})
}

// routeTemplate returns the low-cardinality matched route pattern via chi's
// RouteContext when available, falling back to the raw path otherwise.
func routeTemplate(r *http.Request) string {
	if rctx := chiRouteContext(r); rctx != "" {
		return rctx
	}
	return r.URL.Path
}

// recordAuthOutcome emits the auth attempt outcome counter. denyReason is a
// low-cardinality reason code (e.g. "invalid_token", "expired", "scope") and
// should be empty for allowed attempts.
func recordAuthOutcome(ctx context.Context, allowed bool, denyReason string) {
	outcome := "allowed"
	if !allowed {
		outcome = "denied"
	}

	attrs := []attribute.KeyValue{attribute.String("outcome", outcome)}
	if !allowed {
		attrs = append(attrs, attribute.String("reason", denyReason))
	}

	authAttemptsCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// statusCodeAttr is a small helper kept for potential future call sites
// needing string status codes without repeating strconv.Itoa inline.
func statusCodeAttr(status int) string {
	return strconv.Itoa(status)
}
