// ----------------------------------------------------------------------------
// HTTP saturation gauges — emits http.server.active_requests and
// http.server.worker_pool.size observable gauges for the Worker Pool
// Saturation SLI.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"net/http"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	satMeter      = otel.Meter("go-rest-api/saturation")
	activeReqsVal int64 // atomically incremented/decremented
)

// registerSaturationGauges registers the observable gauges for active requests
// and worker pool size. Call once after the MeterProvider is set globally.
func registerSaturationGauges(workerPoolSize int) error {
	activeRequests, err := satMeter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
	)
	if err != nil {
		return err
	}

	poolSize, err := satMeter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size"),
	)
	if err != nil {
		return err
	}

	_, err = satMeter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequests, atomic.LoadInt64(&activeReqsVal))
		o.ObserveInt64(poolSize, int64(workerPoolSize))
		return nil
	}, activeRequests, poolSize)
	return err
}

// activeRequestsMiddleware tracks in-flight requests for the saturation gauge.
func activeRequestsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeReqsVal, 1)
		defer atomic.AddInt64(&activeReqsVal, -1)
		next.ServeHTTP(w, r)
	})
}
