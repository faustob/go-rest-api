// ----------------------------------------------------------------------------
// Active-request and worker-pool-size observable gauges (saturation SLI)
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// activeRequestCount is incremented/decremented by the saturation middleware.
var activeRequestCount int64

// registerSaturationGauges registers observable gauges for in-flight requests
// and the configured worker pool size. Call once after the MeterProvider is set.
func registerSaturationGauges() error {
	m := otel.Meter("go-rest-api/saturation")

	activeReqs, err := m.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of HTTP requests currently being processed"),
	)
	if err != nil {
		return err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeReqs, atomic.LoadInt64(&activeRequestCount))
		return nil
	}, activeReqs)
	return err
}
