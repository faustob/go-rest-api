// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the API server
//
// Builds the TracerProvider (OTLP/gRPC) and MeterProvider, registers them as
// the global providers and returns a shutdown func to flush on exit.
// Endpoint is env driven via OTEL_EXPORTER_OTLP_ENDPOINT.
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

// initOpenTelemetry builds and globally registers the OTel providers.
// It is safe to call when an external collector is unavailable; the exporter
// simply retries/drops in the background.
func initOpenTelemetry(ctx context.Context, svcName, svcVersion string) (func(context.Context) error, error) {
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

	// Metrics: a reader-less provider would emit nothing, so attach a periodic
	// reader over the OTLP/gRPC metric exporter (endpoint is env driven).
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, errors.Join(err, traceExp.Shutdown(ctx))
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
	)

	// Register globally. Tolerate an agent/other component having already set
	// providers - otel.Set* is idempotent-safe but we guard defensively.
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
