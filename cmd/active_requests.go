// ----------------------------------------------------------------------------
// Active-request and worker-pool-size gauges for HTTP saturation SLI.
// Registered at startup via init(); instruments are observable gauges that
// read from the package-level atomics updated by the otelhttp middleware.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// activeReqCount is incremented/decremented by the saturation middleware.
var activeReqCount int64

// workerPoolSize is the configured maximum number of concurrent workers.
// Defaults to 0 (unbounded); set via WORKER_POOL_SIZE env var at startup.
var workerPoolSize int64

func init() {
	m := otel.Meter("go-rest-api/saturation")

	activeGauge, err := m.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of HTTP requests currently being processed"),
	)
	if err != nil {
		panic(err)
	}

	poolGauge, err := m.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured maximum number of concurrent HTTP workers (0 = unbounded)"),
	)
	if err != nil {
		panic(err)
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeGauge, atomic.LoadInt64(&activeReqCount))
		o.ObserveInt64(poolGauge, atomic.LoadInt64(&workerPoolSize))
		return nil
	}, activeGauge, poolGauge)
	if err != nil {
		panic(err)
	}
}
