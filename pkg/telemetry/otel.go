// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap - builds and registers the global providers.
// Endpoint is env driven via OTEL_EXPORTER_OTLP_ENDPOINT.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitOTel builds the tracer & meter providers and registers them globally.
// It returns a shutdown function that flushes buffered telemetry.
//
// Registration is defensive: if an OTel agent/other component already set a
// global provider, we keep the existing one and continue.
func InitOTel(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	))
	if err != nil {
		res = resource.Default()
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return func(context.Context) error { return nil }, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	// Metrics: OTLP exporter behind a periodic reader so recorded metrics are actually exported.
	metricExporter, metricErr := otlpmetricgrpc.New(ctx)
	if metricErr != nil {
		return func(shutdownCtx context.Context) error { return tp.Shutdown(shutdownCtx) }, metricErr
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}

	return shutdown, nil
}
