// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2024
// Licensed under the MIT License.
//
// Shared OpenTelemetry helpers — meter/tracer getters and custom instruments.
// Uses the globally-registered SDK providers (registered in cmd/otel.go).
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	scopeName    = "github.com/benc-uk/go-rest-api"
	p99BudgetMs  = 750 // milliseconds — P99 SLO budget
)

// Meter returns the application-scoped OTel meter.
func Meter() metric.Meter {
	return otel.GetMeterProvider().Meter(scopeName)
}

// Tracer returns the application-scoped OTel tracer.
func Tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(scopeName)
}

// ---------------------------------------------------------------------------
// Instruments — created once at package init via the global provider.
// ---------------------------------------------------------------------------

var (
	// auth.attempts — outcome counter for authentication SLI
	authAttemptsCounter metric.Int64Counter

	// flow.outcomes — terminal outcome counter for E2E flow success SLI
	flowOutcomesCounter metric.Int64Counter

	// flow.entries — entry counter for E2E flow throughput SLI
	flowEntriesCounter metric.Int64Counter

	// flow.duration — entry-to-terminal histogram for flow freshness SLI (seconds)
	flowDurationHistogram metric.Float64Histogram

	// flow.validation.outcomes — per-step validation outcome counter
	flowValidationCounter metric.Int64Counter
)

// Init creates all metric instruments against the globally-registered SDK
// MeterProvider. It MUST be called from main() after initOTel() has registered
// the SDK via otel.SetMeterProvider — never at package-init time, because the
// global provider is a no-op until SetMeterProvider is called.
func Init() {
	m := Meter()

	var err error

	authAttemptsCounter, err = m.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total authentication/authorisation decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create auth.attempts counter: %v", err)
	}

	flowOutcomesCounter, err = m.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create flow.outcomes counter: %v", err)
	}

	flowEntriesCounter, err = m.Int64Counter(
		"flow.entries",
		metric.WithDescription("Number of times the primary flow entry point is invoked"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create flow.entries counter: %v", err)
	}

	flowDurationHistogram, err = m.Float64Histogram(
		"flow.duration",
		metric.WithDescription("Wall-clock time from flow entry to terminal state"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120),
	)
	if err != nil {
		log.Printf("### OTel: failed to create flow.duration histogram: %v", err)
	}

	flowValidationCounter, err = m.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation outcomes within the primary flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create flow.validation.outcomes counter: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Recording helpers — called from handler and middleware code.
// ---------------------------------------------------------------------------

// RecordAuthAttempt records one authentication decision.
// outcome: "allowed" | "denied"
// reason:  denial reason (empty string for allowed)
func RecordAuthAttempt(ctx context.Context, outcome, reason string) {
	if authAttemptsCounter == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}

	if reason != "" {
		attrs = append(attrs, attribute.String("denial.reason", reason))
	}

	authAttemptsCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordFlowOutcome records the terminal outcome of a primary flow invocation.
// outcome: "success" | "failure"
func RecordFlowOutcome(ctx context.Context, outcome, route string) {
	if flowOutcomesCounter == nil {
		return
	}

	flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("http.route", route),
	))
}

// RecordFlowEntry increments the flow-entry counter (throughput SLI).
func RecordFlowEntry(ctx context.Context, route string) {
	if flowEntriesCounter == nil {
		return
	}

	flowEntriesCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
	))
}

// RecordFlowDuration records the entry-to-terminal wall-clock duration (freshness SLI).
func RecordFlowDuration(ctx context.Context, d time.Duration, route string) {
	if flowDurationHistogram == nil {
		return
	}

	flowDurationHistogram.Record(ctx, d.Seconds(), metric.WithAttributes(
		attribute.String("http.route", route),
	))
}

// RecordValidationOutcome records a per-step validation outcome.
// outcome: "passed" | "failed"
func RecordValidationOutcome(ctx context.Context, outcome, route string) {
	if flowValidationCounter == nil {
		return
	}

	flowValidationCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("http.route", route),
	))
}

// P99BudgetMs returns the configured P99 latency budget in milliseconds.
func P99BudgetMs() int64 {
	return p99BudgetMs
}
