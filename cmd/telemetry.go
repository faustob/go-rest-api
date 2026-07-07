// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Telemetry helpers — instruments for SLIs beyond what otelhttp auto-emits.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// p99BudgetSeconds is the P99 latency SLO budget (750 ms).
	p99BudgetSeconds = 0.750
)

// activeRequestCount tracks in-flight HTTP requests for the saturation SLI.
var activeRequestCount int64

// thingAPITelemetry holds all custom metric instruments for the ThingAPI.
type thingAPITelemetry struct {
	// auth.attempts — outcome counter for the auth-failure-rate SLI.
	authAttempts metric.Int64Counter

	// flow.outcomes — terminal-outcome counter for the e2e flow success SLI.
	flowOutcomes metric.Int64Counter

	// flow.duration — histogram for e2e flow latency / freshness SLIs.
	flowDuration metric.Float64Histogram

	// flow.entries — entry counter for flow throughput SLI.
	flowEntries metric.Int64Counter

	// flow.validation.outcomes — per-step validation outcome counter.
	flowValidationOutcomes metric.Int64Counter
}

// newThingAPITelemetry creates and registers all custom instruments.
// It must be called once after the global MeterProvider is registered.
func newThingAPITelemetry() (*thingAPITelemetry, error) {
	meter := otel.Meter("github.com/benc-uk/go-rest-api")

	authAttempts, err := meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total authentication/authorisation decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, err
	}

	flowOutcomes, err := meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return nil, err
	}

	flowDuration, err := meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary request flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	flowEntries, err := meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Number of times the primary flow entry point was invoked"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return nil, err
	}

	flowValidationOutcomes, err := meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcome of each validation step in the primary flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return nil, err
	}

	// Register observable gauges for the saturation SLI.
	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	_, err = meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequestCount))
			return nil
		},
		activeRequestsGauge,
	)
	if err != nil {
		return nil, err
	}

	return &thingAPITelemetry{
		authAttempts:           authAttempts,
		flowOutcomes:           flowOutcomes,
		flowDuration:           flowDuration,
		flowEntries:            flowEntries,
		flowValidationOutcomes: flowValidationOutcomes,
	}, nil
}

// Attribute key constants — kept here so call-sites stay readable.
var (
	attrOutcome    = attribute.Key("outcome")
	attrDenyReason = attribute.Key("deny.reason")
	attrFlowID     = attribute.Key("flow.id")
	attrStep       = attribute.Key("validation.step")
)
