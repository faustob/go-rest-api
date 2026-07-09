// ----------------------------------------------------------------------------
// Shared metric instrument definitions for the go-rest-api service.
//
// Instruments are created once here against the global Meter and recorded
// at call sites (server.go middleware, routes.go handlers).
// ----------------------------------------------------------------------------

package otelconfig

import (
	"net/http"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/benc-uk/go-rest-api"

// Instruments are created lazily, on first use, rather than at package
// var-init time — otel.Meter()/instrument creation must happen AFTER
// SetupOTelSDK has registered the global MeterProvider in main(), otherwise
// they bind to the no-op meter and never emit.
var (
	instrOnce sync.Once

	requestOutcomeCounter  metric.Int64Counter
	authAttemptCounter     metric.Int64Counter
	flowOutcomeCounter     metric.Int64Counter
	flowDurationHistogram  metric.Float64Histogram
)

func initInstruments() {
	meter := otel.Meter(meterName)

	var err error

	// RequestOutcomeCounter counts inbound HTTP requests by route and outcome
	// class (success / client_error / server_error), supporting the availability
	// and error-rate SLIs without scanning traces.
	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcome.total",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(err)
	}

	// AuthAttemptCounter counts authentication/authorization decisions, tagged
	// with outcome (allowed/denied) and reason for denial, supporting the
	// authentication failure rate SLI.
	authAttemptCounter, err = meter.Int64Counter(
		"auth.attempts.total",
		metric.WithDescription("Count of authentication attempts by outcome and reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		panic(err)
	}

	// FlowOutcomeCounter counts terminal outcomes of the primary end-to-end
	// request flow (the whole HTTP request lifecycle), supporting the flow
	// success-rate and throughput SLIs.
	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes.total",
		metric.WithDescription("Count of primary flow terminal outcomes"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		panic(err)
	}

	// FlowDurationHistogram records entry-to-terminal duration of the primary
	// flow, supporting the flow latency P95 and freshness SLIs.
	flowDurationHistogram, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary request flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(err)
	}
}

// ensureInstruments guarantees the instruments are created exactly once,
// lazily, on first record call (after main() has registered the global
// MeterProvider).
func ensureInstruments() {
	instrOnce.Do(initInstruments)
}

// outcomeClass buckets an HTTP status code into a low-cardinality class.
func outcomeClass(statusCode int) string {
	switch {
	case statusCode >= 500:
		return "server_error"
	case statusCode >= 400:
		return "client_error"
	default:
		return "success"
	}
}

// statusRecorder wraps http.ResponseWriter to capture the written status
// code while preserving the optional interfaces callers may rely on.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
	wrote      bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.statusCode = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.statusCode = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// Flush preserves http.Flusher support (SSE / streaming) if the wrapped
// ResponseWriter implements it.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// AuthMetricsMiddleware records authentication outcome and overall request
// outcome/duration metrics for routes it wraps (the protected route group).
// It never alters control flow or the response — it only observes the
// status code the downstream handlers/middleware ultimately write.
func AuthMetricsMiddleware(next http.Handler) http.Handler {
	ensureInstruments()

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, req)

		statusCode := rec.statusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		route := req.URL.Path

		class := outcomeClass(statusCode)

		// Authentication outcome: the jwtValidator.Middleware runs before this
		// middleware in the chain, so a 401/403 here reflects an auth denial.
		authOutcome := "allowed"
		reason := "none"
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			authOutcome = "denied"
			reason = "unauthorized"
		}

		authAttemptCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("auth.outcome", authOutcome),
			attribute.String("auth.reason", reason),
		))

		requestOutcomeCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("http.request.method", req.Method),
			attribute.String("outcome.class", class),
		))

		flowOutcomeCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("outcome.class", class),
		))
	})
}
