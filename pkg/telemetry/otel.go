// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap: builds the SDK (traces + metrics) and registers it
// as the GLOBAL OpenTelemetry instance. Called once from main().
//
// Endpoint is env driven via OTEL_EXPORTER_OTLP_ENDPOINT.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ShutdownFunc flushes and stops the SDK providers created by InitOTel.
type ShutdownFunc func(ctx context.Context) error

// InitOTel builds the trace + metric providers and registers them globally.
// It is safe to call when an external collector/agent is not configured; a
// failure to reach the endpoint does not prevent the app from starting.
func InitOTel(ctx context.Context, serviceName, serviceVersion string) (ShutdownFunc, error) {
	if envName := os.Getenv("OTEL_SERVICE_NAME"); envName != "" {
		serviceName = envName
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		res = resource.Default()
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	// Metrics: a periodic reader with no explicit exporter would drop data, so we
	// reuse the OTLP gRPC connection settings via the standard metric exporter.
	metricExporter, err := newMetricExporter(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)

		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	// Register defensively: if an agent/other bootstrap already set providers,
	// otel.Set* simply overwrites; we guard against panics from any provider.
	setGlobals(tp, mp)

	return func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}, nil
}

func setGlobals(tp *sdktrace.TracerProvider, mp *sdkmetric.MeterProvider) {
	defer func() {
		// Tolerate an already-registered global provider (e.g. external bootstrap)
		_ = recover()
	}()

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}
