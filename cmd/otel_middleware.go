// ----------------------------------------------------------------------------
// OpenTelemetry middleware helpers
// — saturation tracking (active-request gauge)
// — auth-attempt counter wired into the JWT validation path
// — flow entry/outcome recording wired into every request
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// SaturationMiddleware increments/decrements the activeRequestCount gauge so
// the http.server.active_requests observable gauge always reflects in-flight
// requests (saturation SLI).
func SaturationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequestCount, 1)
		defer atomic.AddInt64(&activeRequestCount, -1)
		next.ServeHTTP(w, r)
	})
}

// FlowMiddleware records flow entry, outcome, and duration for every request
// (flow throughput, success-rate, latency, and freshness SLIs).
func FlowMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Flow-entry counter (throughput SLI)
		if flowOutcomeCounter != nil {
			flowOutcomeCounter.Add(r.Context(), 0, metric.WithAttributes(
				attribute.String("outcome", "started"),
			))
		}

		srw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(srw, r)

		duration := time.Since(start).Seconds()
		outcome := "success"
		if srw.status >= 500 {
			outcome = "error"
		} else if srw.status >= 400 {
			outcome = "client_error"
		}

		RecordFlowOutcome(r.Context(), outcome, duration)
	})
}

// RecordAuthAttempt records a single JWT authentication attempt with its outcome.
// outcome should be "allowed" or "denied"; reason is the denial reason (empty on success).
func RecordAuthAttempt(ctx context.Context, outcome, reason string) {
	if authAttemptCounter == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}
	if reason != "" {
		attrs = append(attrs, attribute.String("denial.reason", reason))
	}
	authAttemptCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
