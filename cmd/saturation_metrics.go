// saturation_metrics.go — HTTP worker pool saturation gauges.
// Registers observable gauges for http.server.active_requests and
// http.server.worker_pool.size so the saturation SLI can be computed.
package main

import (
	"context"
	"runtime"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

func registerSaturationMetrics() {
	sat := otel.Meter("go-rest-api/saturation")

	poolSizeGauge, err := sat.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size (GOMAXPROCS)"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return
	}

	_, err = sat.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(poolSizeGauge, int64(runtime.GOMAXPROCS(0)))
		return nil
	}, poolSizeGauge)
	if err != nil {
		_ = err
	}
}
