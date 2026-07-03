// ----------------------------------------------------------------------------
// HTTP middleware: active-request tracking + otelhttp wiring helpers.
// otelhttp emits http.server.request.duration (semconv) automatically.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// activeRequestMiddleware tracks in-flight requests via the UpDownCounter and
// also records flow.outcomes / flow.duration on every request.
func activeRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		if globalMetrics != nil {
			globalMetrics.activeRequests.Add(r.Context(), 1)
			defer globalMetrics.activeRequests.Add(r.Context(), -1)
		}

		// Wrap the ResponseWriter so we can capture the status code.
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		if globalMetrics != nil {
			duration := time.Since(start).Seconds()
			outcome := "success"
			if rw.status >= 500 {
				outcome = "error"
			} else if rw.status >= 400 {
				outcome = "client_error"
			}

			attrs := []attribute.KeyValue{
				attribute.String("outcome", outcome),
				attribute.Int("http.response.status_code", rw.status),
			}

			globalMetrics.flowOutcomes.Add(r.Context(), 1, metric.WithAttributes(attrs...))
			globalMetrics.flowDuration.Record(r.Context(), duration, metric.WithAttributes(attrs...))

			// Slow-request span event for P99 triage (budget: 750 ms).
			const p99BudgetSeconds = 0.750
			if duration > p99BudgetSeconds {
				span := trace.SpanFromContext(r.Context())
				span.AddEvent("slow_request",
					trace.WithAttributes(
						attribute.Float64("handler.duration_s", duration),
						attribute.Int("http.response.status_code", rw.status),
					),
				)
			}
		}
	})
}

// statusRecorder captures the HTTP status code written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// newOtelHandler wraps a chi router with otelhttp so that
// http.server.request.duration (semconv) is emitted automatically.
func newOtelHandler(serviceName string, h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, serviceName)
}
