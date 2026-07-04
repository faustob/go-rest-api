// saturation_observer.go — registers observable gauges for HTTP worker-pool saturation SLI.
// http.server.active_requests is already tracked as an UpDownCounter in routes.go;
// this file adds the http.server.worker_pool.size observable gauge.
package main

import (
	"context"
	"log"
	"runtime"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// registerSaturationObserver registers an observable gauge for the worker pool size.
// Call this once from main() after initOTel().
func registerSaturationObserver() {
	m := otel.Meter("go-rest-api/saturation")

	poolSizeGauge, err := m.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size (GOMAXPROCS)"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		log.Printf("OTel pool size gauge error: %v", err)
		return
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(poolSizeGauge, int64(runtime.GOMAXPROCS(0)))
		return nil
	}, poolSizeGauge)
	if err != nil {
		log.Printf("OTel pool size callback error: %v", err)
	}
}
