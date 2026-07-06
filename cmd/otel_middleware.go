// ----------------------------------------------------------------------------
// OTel active-request tracking middleware and auth-outcome recording helpers.
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

// activeRequestMiddleware increments/decrements the activeRequestCount atomic
// so the saturation gauge always reflects in-flight requests.
func activeRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequestCount, 1)
		defer atomic.AddInt64(&activeRequestCount, -1)
		next.ServeHTTP(w, r)
	})
}

// RecordAuthAttempt records a single authentication attempt with the given
// outcome ("allowed" or "denied") and optional denial reason.
// Call this from the JWT validator middleware or any auth decision point.
func RecordAuthAttempt(ctx context.Context, outcome, reason string) {
	if authAttempts == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}
	if reason != "" {
		attrs = append(attrs, attribute.String("denial.reason", reason))
	}
	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordFlowOutcome records the terminal outcome of the primary E2E flow and
// its total duration. Call this at the point where a request flow completes.
func RecordFlowOutcome(ctx context.Context, outcome string, start time.Time) {
	if flowOutcomes == nil || flowDuration == nil {
		return
	}
	elapsed := time.Since(start).Seconds()
	attrs := metric.WithAttributes(attribute.String("outcome", outcome))
	flowOutcomes.Add(ctx, 1, attrs)
	flowDuration.Record(ctx, elapsed, attrs)
}

// RecordValidationOutcome records the outcome of a single validation step.
func RecordValidationOutcome(ctx context.Context, step, outcome string) {
	if flowValidationOutcomes == nil {
		return
	}
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("step", step),
		attribute.String("outcome", outcome),
	))
}
