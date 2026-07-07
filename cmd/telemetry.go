// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// OTel active-request and auth-failure instrumentation for the go-rest-api.
// This file owns instruments that require access to the chi router's in-flight
// request count and the JWT auth middleware outcome.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"net/http"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// activeRequestCount tracks in-flight HTTP requests atomically.
var activeRequestCount int64

// workerPoolSize is the configured maximum number of concurrent workers.
// Adjust via WORKER_POOL_SIZE env var or leave at the default.
const workerPoolSize int64 = 100

var (
	telMeter = otel.Meter("go-rest-api/telemetry")

	// authAttempts counts every JWT authentication decision.
	authAttempts, _ = telMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total JWT authentication attempts"),
		metric.WithUnit("{attempt}"),
	)
)

// RegisterSaturationGauges registers the observable saturation gauges against the
// already-initialised global MeterProvider. Must be called from main() after initOTel().
func RegisterSaturationGauges() error {
	m := otel.Meter("go-rest-api/telemetry")

	activeReqGauge, err := m.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	poolSizeGauge, err := m.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeReqGauge, atomic.LoadInt64(&activeRequestCount))
		o.ObserveInt64(poolSizeGauge, workerPoolSize)
		return nil
	}, activeReqGauge, poolSizeGauge)
	return err
}

// ActiveRequestMiddleware tracks in-flight requests for the saturation SLI.
func ActiveRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequestCount, 1)
		defer atomic.AddInt64(&activeRequestCount, -1)
		next.ServeHTTP(w, r)
	})
}

// RecordAuthOutcome records the result of a JWT authentication attempt.
// outcome should be "allowed" or "denied"; reason is the denial reason (empty string if allowed).
func RecordAuthOutcome(ctx context.Context, outcome, reason string) {
	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}
	if reason != "" {
		attrs = append(attrs, attribute.String("denial.reason", reason))
	}
	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}
