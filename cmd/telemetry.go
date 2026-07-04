// ----------------------------------------------------------------------------
// Shared OpenTelemetry instruments used across the application.
// All instruments are created once here and recorded at their call sites.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// appMetrics holds every OTel instrument used by the application.
type appMetrics struct {
	// http.server.active_requests — UpDownCounter for in-flight requests (saturation SLI)
	activeRequests metric.Int64UpDownCounter
	// auth.attempts — Counter for authentication decisions (auth-failure-rate SLI)
	authAttempts metric.Int64Counter
	// flow.outcomes — Counter for E2E flow terminal outcomes
	flowOutcomes metric.Int64Counter
	// flow.duration — Histogram for E2E flow latency (seconds)
	flowDuration metric.Float64Histogram
	// flow.validation.outcomes — Counter for per-step validation outcomes
	flowValidationOutcomes metric.Int64Counter
}

// globalMetrics is the singleton set of instruments, initialised once in
// initMetrics() which is called from main() after the SDK is registered.
var globalMetrics *appMetrics

// initMetrics creates all application-level metric instruments.
// Must be called AFTER initOTel() so the global MeterProvider is live.
func initMetrics() {
	m := otel.Meter("go-rest-api")

	activeReqs, err := m.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests (saturation signal)"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Fatalf("create http.server.active_requests: %v", err)
	}

	authAtt, err := m.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total authentication attempts, labelled by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Fatalf("create auth.attempts: %v", err)
	}

	flowOut, err := m.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary E2E business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		log.Fatalf("create flow.outcomes: %v", err)
	}

	flowDur, err := m.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Fatalf("create flow.duration: %v", err)
	}

	flowValOut, err := m.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcomes of per-step validation within the primary flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		log.Fatalf("create flow.validation.outcomes: %v", err)
	}

	globalMetrics = &appMetrics{
		activeRequests:         activeReqs,
		authAttempts:           authAtt,
		flowOutcomes:           flowOut,
		flowDuration:           flowDur,
		flowValidationOutcomes: flowValOut,
	}
}

// RecordAuthAttempt records one authentication decision.
// outcome: "success" | "denied"
// reason:  denial reason (e.g. "invalid_token", "expired", "missing_scope") or "" on success.
func RecordAuthAttempt(ctx context.Context, outcome, reason string) {
	if globalMetrics == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}
	if reason != "" {
		attrs = append(attrs, attribute.String("error.type", reason))
	}
	globalMetrics.authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordFlowOutcome records the terminal outcome of one E2E flow execution.
// outcome: "success" | "failure"
func RecordFlowOutcome(ctx context.Context, outcome string, durationSec float64) {
	if globalMetrics == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("outcome", outcome))
	globalMetrics.flowOutcomes.Add(ctx, 1, attrs)
	globalMetrics.flowDuration.Record(ctx, durationSec, attrs)
}

// RecordValidationOutcome records the outcome of one validation step.
// step: name of the validation step; outcome: "passed" | "failed".
func RecordValidationOutcome(ctx context.Context, step, outcome string) {
	if globalMetrics == nil {
		return
	}
	globalMetrics.flowValidationOutcomes.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("step", step),
			attribute.String("outcome", outcome),
		),
	)
}
