// ----------------------------------------------------------------------------
// Custom telemetry middleware for go-rest-api
// Wraps each request to:
//   • track http.server.active_requests (UpDownCounter)
//   • record auth.attempts outcome via RecordAuthAttempt
//   • record flow entry, outcome, and duration for the primary business flow
//   • record flow.validation.outcomes for JWT validation steps
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// activeRequestsMiddleware tracks in-flight requests via the
// http.server.active_requests UpDownCounter (saturation SLI).
func activeRequestsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if globalMetrics != nil {
			globalMetrics.activeRequests.Add(r.Context(), 1)
			defer globalMetrics.activeRequests.Add(r.Context(), -1)
		}
		next.ServeHTTP(w, r)
	})
}

// flowTelemetryMiddleware records flow entry, outcome, and duration for every
// request that reaches a business route (primary flow SLIs).
// It also adds a root span for the flow so downstream spans nest correctly.
func flowTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Record flow entry (throughput SLI)
		RecordFlowEntry(r.Context())

		// Start a root flow span for E2E tracing / latency SLI
		tracer := otel.Tracer("go-rest-api")
		ctx, span := tracer.Start(r.Context(), "primary-flow",
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		// Wrap the ResponseWriter so we can read the status code
		wrapped := newStatusRecorder(w)
		next.ServeHTTP(wrapped, r.WithContext(ctx))

		duration := time.Since(start).Seconds()
		statusCode := wrapped.status
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		outcome := "success"
		if statusCode >= 500 {
			outcome = "failure"
		}

		// flow.outcomes counter (E2E success-rate SLI)
		RecordFlowOutcome(ctx, outcome)
		// flow.duration histogram (E2E latency + freshness SLIs)
		RecordFlowDuration(ctx, duration, outcome)

		// Annotate the flow span with outcome
		span.SetAttributes(
			attribute.String("flow.outcome", outcome),
			attribute.Int("http.response.status_code", statusCode),
		)
	})
}

// jwtValidationTelemetryMiddleware wraps the JWT validator middleware to record
// auth.attempts and flow.validation.outcomes (auth-failure-rate + validation SLIs).
// It must be placed AFTER the JWT validator in the middleware chain so it can
// observe the response status set by the validator.
func jwtValidationTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := newStatusRecorder(w)
		next.ServeHTTP(wrapped, r)

		statusCode := wrapped.status
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			RecordAuthAttempt(r.Context(), "denied", "jwt_rejected")
			RecordValidationOutcome(r.Context(), "jwt", "failed")
		} else {
			RecordAuthAttempt(r.Context(), "success", "")
			RecordValidationOutcome(r.Context(), "jwt", "passed")
		}
	})
}

// statusRecorder is a minimal http.ResponseWriter wrapper that captures the
// HTTP status code written by the handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w}
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Ensure statusRecorder satisfies http.Flusher so streaming responses work.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// workerPoolSizeGauge registers an observable gauge for the saturation SLI.
// Call this once after initMetrics; it uses the global MeterProvider.
func workerPoolSizeGauge(workerPoolSize int) error {
	m := otel.Meter("go-rest-api")

	poolSizeGauge, err := m.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(poolSizeGauge, int64(workerPoolSize))
		return nil
	}, poolSizeGauge)
	return err
}
