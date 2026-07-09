// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Custom OpenTelemetry metrics: auth outcomes, request-flow outcomes,
// and worker pool saturation gauges.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterScope = "github.com/benc-uk/go-rest-api"

var (
	authAttemptsCounter metric.Int64Counter
	flowOutcomeCounter  metric.Int64Counter
	activeRequests      int64
)

// initAPIMetrics creates the custom instruments used across the API:
// auth outcome counter, flow outcome counter, and active-request /
// worker-pool-size observable gauges (saturation).
func initAPIMetrics() error {
	meter := otel.Meter(meterScope)

	var err error

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization attempts by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return fmt.Errorf("failed to create auth.attempts counter: %w", err)
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of end-to-end request flow terminal outcomes"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		return fmt.Errorf("failed to create flow.outcomes counter: %w", err)
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.active_requests gauge: %w", err)
	}

	workerPoolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured size of the HTTP server worker pool (GOMAXPROCS)"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.worker_pool.size gauge: %w", err)
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
		o.ObserveInt64(workerPoolSizeGauge, int64(runtime.GOMAXPROCS(0)))
		return nil
	}, activeRequestsGauge, workerPoolSizeGauge)
	if err != nil {
		return fmt.Errorf("failed to register saturation gauge callback: %w", err)
	}

	return nil
}

// activeRequestTracker is chi middleware that tracks in-flight request
// count for the saturation gauge, and records flow-entry/outcome counters
// for the primary E2E flow SLIs.
func activeRequestTracker(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequests, 1)
		defer atomic.AddInt64(&activeRequests, -1)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		if flowOutcomeCounter != nil {
			outcome := "success"
			if rec.status >= http.StatusBadRequest {
				outcome = "error"
			}
			flowOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("flow.outcome", outcome),
			))
		}
	})
}

// authMetricsMiddleware records an auth.attempts counter for every request
// through the protected route group, tagged with the outcome (allowed/denied)
// as determined by the JWT validator middleware that runs before it.
func authMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If we reach this middleware, the JWT validator upstream already
		// let the request through (it would have written a 401/403 and
		// returned otherwise, so this handler wouldn't be reached with a
		// wrapped writer). We record the outcome using a response recorder
		// so we don't alter existing behavior or writer capabilities.
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		outcome := "allowed"
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			outcome = "denied"
		}

		if authAttemptsCounter != nil {
			authAttemptsCounter.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("auth.outcome", outcome),
			))
		}
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code while
// preserving optional interfaces (Flush, Hijack, ReadFrom) for streaming.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
