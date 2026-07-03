// ----------------------------------------------------------------------------
// Telemetry instruments — all metric instruments are defined here exactly once.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"runtime"

	"go.opentelemetry.io/otel/metric"
)

// appMetrics holds every instrument used by the application.
type appMetrics struct {
	// auth.attempts — counts every JWT auth decision
	authAttempts metric.Int64Counter

	// flow.outcomes — counts terminal flow outcomes
	flowOutcomes metric.Int64Counter

	// flow.duration — end-to-end flow latency histogram (seconds)
	flowDuration metric.Float64Histogram

	// flow.validation.outcomes — per-step validation pass/fail
	flowValidationOutcomes metric.Int64Counter

	// http.server.active_requests — in-flight requests (UpDownCounter)
	activeRequests metric.Int64UpDownCounter
}

// globalMetrics is the singleton set of instruments, initialised in initMetrics.
var globalMetrics *appMetrics

// initMetrics creates all instruments against the global Meter.
// Must be called after initOTel() so the global MeterProvider is registered.
func initMetrics() error {
	m := Meter()

	authAttempts, err := m.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total JWT authentication/authorisation decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	flowOutcomes, err := m.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowDuration, err := m.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end primary flow latency"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	flowValidationOutcomes, err := m.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation pass/fail outcomes"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return err
	}

	activeRequests, err := m.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of HTTP requests currently being processed"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	// Observable gauge for worker pool size — GOMAXPROCS is the closest proxy
	// for Go's scheduler concurrency ceiling (no fixed HTTP worker pool).
	poolSizeGauge, err := m.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Scheduler concurrency ceiling (GOMAXPROCS)"),
		metric.WithUnit("{goroutine}"),
	)
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(poolSizeGauge, int64(runtime.GOMAXPROCS(0)))
		return nil
	}, poolSizeGauge)
	if err != nil {
		return err
	}

	globalMetrics = &appMetrics{
		authAttempts:           authAttempts,
		flowOutcomes:           flowOutcomes,
		flowDuration:           flowDuration,
		flowValidationOutcomes: flowValidationOutcomes,
		activeRequests:         activeRequests,
	}
	return nil
}
