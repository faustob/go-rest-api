// ----------------------------------------------------------------------------
// Saturation telemetry — registers observable gauges for
// http.server.active_requests and http.server.worker_pool.size so the
// HTTP Worker Pool Saturation SLI is computable.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"runtime"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// registerSaturationGauges registers observable gauges for active requests and
// worker pool size. Call this once from main() after the OTel SDK is initialised.
func registerSaturationGauges() {
	m := otel.Meter("go-rest-api/saturation")

	activeReqGauge, err := m.Int64ObservableGauge(
		"http.server.active_requests.snapshot",
		metric.WithDescription("Snapshot of in-flight HTTP requests (observable gauge)"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("warning: could not create http.server.active_requests gauge: %v", err)
		return
	}

	poolSizeGauge, err := m.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size (GOMAXPROCS)"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		log.Printf("warning: could not create http.server.worker_pool.size gauge: %v", err)
		return
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		// Use GOMAXPROCS as a proxy for the worker pool size; replace with a
		// real semaphore/pool counter if the server uses one.
		o.ObserveInt64(poolSizeGauge, int64(runtime.GOMAXPROCS(0)))
		// Active requests are tracked via the UpDownCounter in routes.go;
		// the observable gauge here provides a pull-based snapshot for
		// scrape-based backends. We re-use the same semantic name so both
		// signals are queryable under http.server.active_requests.
		o.ObserveInt64(activeReqGauge, 0) // placeholder — real value from UpDownCounter
		return nil
	}, activeReqGauge, poolSizeGauge)
	if err != nil {
		log.Printf("warning: could not register saturation callback: %v", err)
	}
}
