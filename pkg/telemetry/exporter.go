// ----------------------------------------------------------------------------
// OTLP metric exporter construction, kept separate for clarity.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func newMetricExporter(ctx context.Context) (sdkmetric.Exporter, error) {
	return otlpmetricgrpc.New(ctx)
}
