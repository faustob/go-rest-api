// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Single package-level meter for the service, and all HTTP server / auth /
// flow instruments created from it.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"log"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// meter is the ONE meter for this service; every instrument and every
// RegisterCallback must be created from this same meter.
var meter = otel.Meter("github.com/benc-uk/go-rest-api")

var (
	httpRequestDuration metric.Float64Histogram
	authAttempts        metric.Int64Counter
	flowOutcomes        metric.Int64Counter
	flowEntries         metric.Int64Counter
	flowDuration        metric.Float64Histogram
	validationOutcomes  metric.Int64Counter

	activeRequests int64 // in-flight request counter, read by the observable gauge callback
)

// MaxWorkers is the configured worker pool size, used as the denominator for
// the saturation SLI. Go's net/http server does not have an explicit worker
// pool, so we surface GOMAXPROCS-derived capacity as a best-effort proxy.
var maxWorkers int64 = 100

func init() {
	var err error

	httpRequestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create http.server.request.duration histogram: %v", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create auth.attempts counter: %v", err)
	}

	flowOutcomes, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary end-to-end request flow"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create flow.outcomes counter: %v", err)
	}

	flowEntries, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of entries into the primary end-to-end request flow"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create flow.entries counter: %v", err)
	}

	flowDuration, err = meter.Float64Histogram(
		"flow.duration",
		 metric.WithDescription("End-to-end duration of the primary request flow, entry to terminal state"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create flow.duration histogram: %v", err)
	}

	validationOutcomes, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcome of per-step request validation within the primary flow"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create flow.validation.outcomes counter: %v", err)
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create http.server.active_requests gauge: %v", err)
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create http.server.worker_pool.size gauge: %v", err)
	}

	if activeRequestsGauge != nil && poolSizeGauge != nil {
		_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
			o.ObserveInt64(poolSizeGauge, atomic.LoadInt64(&maxWorkers))
			return nil
		}, activeRequestsGauge, poolSizeGauge)
		if err != nil {
			log.Printf("### ⚠️  Failed to register saturation gauge callback: %v", err)
		}
	}
}
