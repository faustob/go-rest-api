// ----------------------------------------------------------------------------
// OTel middleware for chi: wraps every request to
//   • track http.server.active_requests (saturation SLI)
//   • record flow.outcomes and flow.duration (E2E flow SLIs)
//   • record flow.validation.outcomes for JWT validation steps
// http.server.request.duration is emitted automatically by otelhttp.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// otelSaturationMiddleware tracks in-flight requests for the saturation SLI.
func otelSaturationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if globalMetrics == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		globalMetrics.activeRequests.Add(ctx, 1, metric.WithAttributes())
		defer globalMetrics.activeRequests.Add(ctx, -1, metric.WithAttributes())
		next.ServeHTTP(w, r)
	})
}

// otelFlowMiddleware records E2E flow outcome and duration for every request.
func otelFlowMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := r.Context()

		// Wrap the ResponseWriter to capture the status code
		wrapped := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		duration := time.Since(start).Seconds()
		outcome := "success"
		if wrapped.status >= 500 {
			outcome = "failure"
		}
		RecordFlowOutcome(ctx, outcome, duration)
	})
}

// statusRecorder is a minimal ResponseWriter wrapper that captures the HTTP
// status code written by the handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
