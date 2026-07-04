// ----------------------------------------------------------------------------
// Shared telemetry instruments for go-rest-api
// All metric instruments are defined here and recorded at their call sites.
// ----------------------------------------------------------------------------

package main

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// p99BudgetSeconds is the P99 latency budget (750 ms). Handlers that exceed
// this threshold add a span event for triage.
const p99BudgetSeconds = 0.750

// thingMetrics holds all metric instruments for the Things API.
type thingMetrics struct {
	// http.server.active_requests — UpDownCounter for in-flight requests
	// (otelhttp already emits this; we expose it here for the saturation SLI).
	activeRequests metric.Int64UpDownCounter

	// auth.attempts — counter for every JWT auth decision
	authAttempts metric.Int64Counter

	// flow.outcomes — counter for E2E business flow terminal outcomes
	flowOutcomes metric.Int64Counter

	// flow.duration — histogram for E2E flow latency (entry-to-terminal)
	flowDuration metric.Float64Histogram

	// flow.entry — counter incremented at every flow entry point
	flowEntry metric.Int64Counter

	// flow.validation.outcomes — counter for per-step validation results
	flowValidationOutcomes metric.Int64Counter
}

// globalMetrics is the package-level instance initialised in initMetrics.
var globalMetrics *thingMetrics

// initMetrics creates all instruments against the global MeterProvider.
// Must be called after initOTel has registered the global provider.
func initMetrics() error {
	m := otel.Meter("go-rest-api")

	activeReqs, err := m.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	authAttempts, err := m.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total JWT authentication attempts"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	flowOutcomes, err := m.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary E2E business flow"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		return err
	}

	flowDuration, err := m.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	flowEntry, err := m.Int64Counter(
		"flow.entry",
		metric.WithDescription("Number of times the primary flow entry point was invoked"),
		metric.WithUnit("{invocation}"),
	)
	if err != nil {
		return err
	}

	flowValidationOutcomes, err := m.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation outcomes for the primary flow"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		return err
	}

	globalMetrics = &thingMetrics{
		activeRequests:         activeReqs,
		authAttempts:           authAttempts,
		flowOutcomes:           flowOutcomes,
		flowDuration:           flowDuration,
		flowEntry:              flowEntry,
		flowValidationOutcomes: flowValidationOutcomes,
	}
	return nil
}

// RecordAuthAttempt records a single JWT authentication decision.
// outcome should be "success" or "denied"; reason is the denial class (e.g.
// "expired_token", "invalid_signature", "missing_scope") — empty on success.
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

// RecordFlowEntry increments the flow entry counter.
func RecordFlowEntry(ctx context.Context) {
	if globalMetrics == nil {
		return
	}
	globalMetrics.flowEntry.Add(ctx, 1)
}

// RecordFlowOutcome records the terminal outcome of a business flow.
// outcome should be "success" or "failure".
func RecordFlowOutcome(ctx context.Context, outcome string) {
	if globalMetrics == nil {
		return
	}
	globalMetrics.flowOutcomes.Add(ctx, 1,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
}

// RecordFlowDuration records the wall-clock duration of a completed flow.
func RecordFlowDuration(ctx context.Context, durationSeconds float64, outcome string) {
	if globalMetrics == nil {
		return
	}
	globalMetrics.flowDuration.Record(ctx, durationSeconds,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
}

// RecordValidationOutcome records a per-step validation result.
// step is the validation step name; outcome is "passed" or "failed".
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
