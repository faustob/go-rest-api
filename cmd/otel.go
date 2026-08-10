// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the API server.
//
// Builds the TracerProvider and MeterProvider, registers them as the GLOBAL
// OpenTelemetry instances and returns a shutdown func that flushes buffered
// telemetry. Endpoint configuration is env driven via OTEL_EXPORTER_OTLP_*.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOpenTelemetry builds and globally registers the OTel SDK providers.
// It is safe to call when an external agent/collector is not reachable; the
// exporter simply retries in the background.
func initOpenTelemetry(ctx context.Context, svcName string, svcVersion string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(svcName),
			semconv.ServiceVersion(svcVersion),
		),
	)
	if err != nil {
		res = resource.Default()
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	// Metrics: export over OTLP/gRPC on a periodic reader so recorded metrics
	// actually leave the process. Endpoint is env driven (OTEL_EXPORTER_OTLP_*).
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Register globally. If an agent already registered providers this simply
	// overrides the process-local delegate; OTel Go tolerates repeated Set calls.
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
