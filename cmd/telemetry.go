// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Custom telemetry instruments for business-level SLIs not covered by
// automatic otelchi HTTP instrumentation: auth outcome, active requests,
// worker pool size, and end-to-end flow outcomes.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Single package-level meter for this service; every instrument and callback
// used across cmd/ must be created from this same meter. It is assigned by
// initTelemetry, which must be called from main() AFTER setupOTelSDK has
// registered the global MeterProvider — otel.Meter called before that point
// (e.g. at package-var/init time) would bind to the no-op default provider.
var meter metric.Meter

var (
	authAttemptsCounter metric.Int64Counter
	flowOutcomeCounter  metric.Int64Counter
	flowEntryCounter    metric.Int64Counter
	flowDurationHist    metric.Float64Histogram
	validationCounter   metric.Int64Counter

	activeRequests int64
	maxWorkers     int64 = 100 // configured worker pool ceiling; adjust as appropriate
)

// recordAuthAttempt increments the auth attempts counter with an outcome attribute.
func recordAuthAttempt(ctx context.Context, outcome string) {
	authAttemptsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// recordFlowEntry increments the flow entries counter.
func recordFlowEntry(ctx context.Context) {
	flowEntryCounter.Add(ctx, 1)
}

// recordFlowOutcome increments the flow outcome counter and records the flow duration.
func recordFlowOutcome(ctx context.Context, outcome string, durationSeconds float64) {
	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	flowDurationHist.Record(ctx, durationSeconds, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// recordValidationOutcome increments the per-step validation outcome counter.
func recordValidationOutcome(ctx context.Context, outcome string) {
	validationCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// incActiveRequests increments the in-flight request gauge value.
func incActiveRequests() {
	atomic.AddInt64(&activeRequests, 1)
}

// decActiveRequests decrements the in-flight request gauge value.
func decActiveRequests() {
	atomic.AddInt64(&activeRequests, -1)
}

// initTelemetry creates the package meter and all instruments, and registers
// the observable-gauge callback. It MUST be called from main() after
// setupOTelSDK has registered the global MeterProvider, otherwise the meter
// binds to the no-op default provider and no telemetry is ever emitted.
func initTelemetry() {
	meter = otel.Meter("go-rest-api")

	var err error

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization attempts by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		panic(err)
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary end-to-end business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		panic(err)
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of entries into the primary end-to-end business flow"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		panic(err)
	}

	flowDurationHist, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow, entry to terminal state"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(err)
	}

	validationCounter, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcomes of per-step request validation within the primary flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		panic(err)
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests currently being served"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(err)
	}

	workerPoolGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured maximum size of the HTTP server worker pool"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		panic(err)
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
		o.ObserveInt64(workerPoolGauge, maxWorkers)
		return nil
	}, activeRequestsGauge, workerPoolGauge)
	if err != nil {
		panic(err)
	}
}
