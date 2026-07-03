// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Instrument definitions — one place for every OTel instrument used by the
// application.  Recording happens at the real call sites (routes.go, main.go).
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"
)

// ---------------------------------------------------------------------------
// Instruments
// ---------------------------------------------------------------------------

// http.server.request.duration — emitted by otelhttp middleware automatically;
// we keep a reference here only for the active-request gauge callback.

// activeRequestsGauge — observable gauge for in-flight HTTP requests.
var activeRequestsGauge metric.Int64ObservableGauge

// workerPoolSizeGauge — observable gauge for the configured worker pool size.
var workerPoolSizeGauge metric.Int64ObservableGauge

// activeRequestCount is incremented/decremented atomically by the
// active-request tracking middleware wired in main.go.
var activeRequestCount int64

// authAttemptsCounter — counts every auth decision (outcome: success/denied).
var authAttemptsCounter metric.Int64Counter

// flowOutcomesCounter — counts terminal flow outcomes (outcome: success/failure).
var flowOutcomesCounter metric.Int64Counter

// flowDurationHistogram — records entry-to-terminal wall-clock duration (s).
var flowDurationHistogram metric.Float64Histogram

// flowValidationOutcomesCounter — counts per-step validation outcomes.
var flowValidationOutcomesCounter metric.Int64Counter

// flowEntryCounter — counts every time the primary flow entry point is invoked.
var flowEntryCounter metric.Int64Counter

// initInstruments creates all instruments against the global Meter.
// Called once from initOTel() after the MeterProvider is registered.
func initInstruments() error {
	var err error

	// --- HTTP saturation gauges ---
	activeRequestsGauge, err = Meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	workerPoolSizeGauge, err = Meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	// Register the callback that reads the atomic counter and a fixed pool size.
	// The pool size is read from the runtime GOMAXPROCS value as a proxy; adjust
	// to your actual worker-pool configuration if you add one.
	_, err = Meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequestCount))
			o.ObserveInt64(workerPoolSizeGauge, workerPoolSize())
			return nil
		},
		activeRequestsGauge,
		workerPoolSizeGauge,
	)
	if err != nil {
		return err
	}

	// --- Auth attempt counter ---
	authAttemptsCounter, err = Meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	// --- Flow outcome counter ---
	flowOutcomesCounter, err = Meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	// --- Flow duration histogram ---
	flowDurationHistogram, err = Meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("Entry-to-terminal wall-clock duration of the primary flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	// --- Flow validation outcome counter ---
	flowValidationOutcomesCounter, err = Meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation outcomes in the primary flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return err
	}

	// --- Flow entry counter ---
	flowEntryCounter, err = Meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Number of times the primary flow entry point is invoked"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	return nil
}
