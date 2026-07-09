package otelconfig

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// newMeterProvider builds a MeterProvider with a PeriodicReader backed by an
// OTLP gRPC metric exporter, so counters/histograms registered against the
// resulting Meter are actually exported. The endpoint is env-driven via the
// standard OTEL_EXPORTER_OTLP_ENDPOINT variable (read by the exporter
// itself), consistent with the trace exporter configuration.
func newMeterProvider(res *resource.Resource) (*metric.MeterProvider, error) {
	ctx := context.Background()

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	mp := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
	)
	return mp, nil
}
