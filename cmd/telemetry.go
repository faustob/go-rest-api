// ----------------------------------------------------------------------------
// Shared telemetry instruments for the go-rest-api service.
// All instruments are obtained from the global MeterProvider so they are
// no-ops until initOtel() has been called.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterScope = "github.com/benc-uk/go-rest-api"

var (
	telOnce sync.Once

	// http.server.active_requests — UpDownCounter tracking in-flight requests.
	// Incremented by the saturation middleware, decremented on completion.
	activeRequestsCounter metric.Int64UpDownCounter

	// auth.attempts — Counter for every JWT auth decision.
	authAttemptsCounter metric.Int64Counter

	// flow.outcomes — Counter for terminal flow outcomes.
	flowOutcomesCounter metric.Int64Counter

	// flow.duration — Histogram for entry-to-terminal wall-clock duration.
	flowDurationHist metric.Float64Histogram

	// flow.validation.outcomes — Counter for per-step validation results.
	flowValidationCounter metric.Int64Counter
)

// initInstruments creates all metric instruments exactly once.
// It is called lazily from the middleware and route helpers below.
func initInstruments() {
	telOnce.Do(func() {
		m := otel.Meter(meterScope)

		var err error

		activeRequestsCounter, err = m.Int64UpDownCounter(
			"http.server.active_requests",
			metric.WithDescription("Number of in-flight HTTP server requests"),
			metric.WithUnit("{request}"),
		)
		if err != nil {
			panic("otel: http.server.active_requests: " + err.Error())
		}

		authAttemptsCounter, err = m.Int64Counter(
			"auth.attempts",
			metric.WithDescription("Count of JWT authentication attempts, tagged by outcome"),
			metric.WithUnit("{attempt}"),
		)
		if err != nil {
			panic("otel: auth.attempts: " + err.Error())
		}

		flowOutcomesCounter, err = m.Int64Counter(
			"flow.outcomes",
			metric.WithDescription("Terminal outcomes of the primary request flow"),
			metric.WithUnit("{flow}"),
		)
		if err != nil {
			panic("otel: flow.outcomes: " + err.Error())
		}

		flowDurationHist, err = m.Float64Histogram(
			"flow.duration",
			metric.WithDescription("Entry-to-terminal wall-clock duration of the primary request flow"),
			metric.WithUnit("s"),
		)
		if err != nil {
			panic("otel: flow.duration: " + err.Error())
		}

		flowValidationCounter, err = m.Int64Counter(
			"flow.validation.outcomes",
			metric.WithDescription("Per-step validation outcomes for the primary request flow"),
			metric.WithUnit("{validation}"),
		)
		if err != nil {
			panic("otel: flow.validation.outcomes: " + err.Error())
		}
	})
}

// ── Active-request saturation tracking ───────────────────────────────────────

var activeReqCount int64 // atomic

// recordRequestStart increments the in-flight counter.
func recordRequestStart(ctx context.Context, route string) {
	initInstruments()
	atomic.AddInt64(&activeReqCount, 1)
	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
	))
}

// recordRequestEnd decrements the in-flight counter and records the flow outcome.
func recordRequestEnd(ctx context.Context, route string, statusCode int, durationSec float64) {
	initInstruments()
	atomic.AddInt64(&activeReqCount, -1)
	activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(
		attribute.String("http.route", route),
	))

	outcome := "success"
	if statusCode >= 500 {
		outcome = "error"
	} else if statusCode >= 400 {
		outcome = "client_error"
	}

	// flow.outcomes counter
	flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("outcome", outcome),
		attribute.Int("http.response.status_code", statusCode),
	))

	// flow.duration histogram
	flowDurationHist.Record(ctx, durationSec, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("outcome", outcome),
	))
}

// ── Auth outcome recording ────────────────────────────────────────────────────

// RecordAuthAttempt records a JWT authentication decision.
// outcome should be "allowed" or "denied"; reason is the denial class (empty on success).
func RecordAuthAttempt(ctx context.Context, outcome, reason string) {
	initInstruments()
	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}
	if reason != "" {
		attrs = append(attrs, attribute.String("denial.reason", reason))
	}
	authAttemptsCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// ── Validation outcome recording ──────────────────────────────────────────────

// RecordValidationOutcome records a per-step validation result.
func RecordValidationOutcome(ctx context.Context, step, outcome string) {
	initInstruments()
	flowValidationCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("validation.step", step),
		attribute.String("outcome", outcome),
	))
}
