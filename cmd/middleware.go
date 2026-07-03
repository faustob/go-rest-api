// ----------------------------------------------------------------------------
// HTTP middleware that records per-request telemetry:
//   - increments httpRequestTotal (availability / throughput SLI)
//   - tracks httpActiveRequests (saturation SLI)
//   - records flow.outcomes and flow.duration (E2E flow SLIs)
//   - records flow.validation.outcomes (validation failure-rate SLI)
//   - adds a span event when handler duration exceeds the P99 budget (750 ms)
// The http.server.request.duration histogram (latency SLIs) is emitted
// automatically by the otelhttp middleware wired in main.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const p99BudgetSeconds = 0.750 // 750 ms P99 budget

// telemetryMiddleware wraps each request to record application-level metrics.
func telemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Track in-flight requests (saturation SLI).
		atomic.AddInt64(&activeRequestsGauge, 1)
		httpActiveRequests.Add(r.Context(), 1)
		defer func() {
			atomic.AddInt64(&activeRequestsGauge, -1)
			httpActiveRequests.Add(r.Context(), -1)
		}()

		// Wrap the ResponseWriter so we can capture the status code.
		wrapped := newStatusRecorder(w)

		// Validation: every inbound request is a validation attempt.
		// The JWT middleware (if present) will record auth.attempts separately;
		// here we record the structural validation outcome.
		flowValidationOutcomes.Add(r.Context(), 1,
			attribute.String("outcome", "attempted"),
			attribute.String("http.request.method", r.Method),
		)

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start).Seconds()
		statusCode := wrapped.status
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		// Determine outcome class.
		outcomeClass := "success"
		if statusCode >= 500 {
			outcomeClass = "error"
		} else if statusCode >= 400 {
			outcomeClass = "client_error"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.Int("http.response.status_code", statusCode),
			attribute.String("outcome", outcomeClass),
		}

		// Availability / throughput SLI counter.
		httpRequestTotal.Add(r.Context(), 1, attrs...)

		// E2E flow outcome counter.
		flowOutcomes.Add(r.Context(), 1,
			attribute.String("outcome", outcomeClass),
			attribute.String("http.request.method", r.Method),
		)

		// E2E flow duration histogram (freshness / latency SLIs).
		flowDuration.Record(r.Context(), duration,
			attribute.String("http.request.method", r.Method),
			attribute.String("outcome", outcomeClass),
		)

		// Validation outcome (pass/fail based on status).
		validationOutcome := "passed"
		if statusCode >= 400 {
			validationOutcome = "failed"
		}
		flowValidationOutcomes.Add(r.Context(), 1,
			attribute.String("outcome", validationOutcome),
			attribute.String("http.request.method", r.Method),
		)

		// P99 slow-request span event (latency P99 SLI triage).
		if duration > p99BudgetSeconds {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow_request",
				trace.WithAttributes(
					attribute.Float64("handler.duration_s", duration),
					attribute.Int("http.response.status_code", statusCode),
					attribute.String("http.request.method", r.Method),
				),
			)
		}
	})
}

// statusRecorder is a minimal ResponseWriter wrapper that captures the HTTP
// status code written by the handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w}
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
