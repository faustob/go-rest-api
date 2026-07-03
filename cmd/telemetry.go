// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Shared OpenTelemetry instruments used across the service.
// Call initTelemetry() once after initOTel() to create all instruments.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const scopeName = "github.com/benc-uk/go-rest-api"

// telemetry holds every OTel instrument used by the service.
type telemetry struct {
	// http.server.request.duration — emitted by otelhttp middleware; kept here
	// so route handlers can read activeRequests for the saturation gauge.
	activeRequests atomic.Int64

	// auth.attempts — outcome counter for JWT auth decisions
	authAttempts metric.Int64Counter

	// flow.outcomes — terminal outcome counter for the primary business flow
	flowOutcomes metric.Int64Counter

	// flow.duration — end-to-end flow latency histogram (seconds)
	flowDuration metric.Float64Histogram

	// flow.validation.outcomes — per-step validation outcome counter
	flowValidationOutcomes metric.Int64Counter
}

// global singleton
var tel *telemetry

// initTelemetry creates all instruments and registers observable gauges.
// Must be called after initOTel() so the global MeterProvider is live.
func initTelemetry() error {
	m := otel.Meter(scopeName)
	t := &telemetry{}

	var err error

	// auth.attempts counter
	t.authAttempts, err = m.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return fmt.Errorf("auth.attempts counter: %w", err)
	}

	// flow.outcomes counter
	t.flowOutcomes, err = m.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of the primary business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return fmt.Errorf("flow.outcomes counter: %w", err)
	}

	// flow.duration histogram (seconds)
	t.flowDuration, err = m.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("flow.duration histogram: %w", err)
	}

	// flow.validation.outcomes counter
	t.flowValidationOutcomes, err = m.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation outcome for the primary business flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return fmt.Errorf("flow.validation.outcomes counter: %w", err)
	}

	// http.server.active_requests — observable gauge backed by atomic counter
	activeGauge, err := m.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of HTTP requests currently being handled"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("http.server.active_requests gauge: %w", err)
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeGauge, t.activeRequests.Load())
		return nil
	}, activeGauge)
	if err != nil {
		return fmt.Errorf("active_requests callback: %w", err)
	}

	tel = t
	return nil
}
