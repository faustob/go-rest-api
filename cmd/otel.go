// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the API server.
//
// Builds the tracer & meter providers and registers them as the GLOBAL OTel
// instances. Exporter endpoint is env driven (OTEL_EXPORTER_OTLP_ENDPOINT).
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOTel builds and globally registers the OpenTelemetry providers.
// It returns a shutdown function which flushes buffered telemetry.
func initOTel(ctx context.Context, svcName string, svcVersion string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(svcName),
			semconv.ServiceVersion(svcVersion),
			attribute.String("service.instance.id", svcName),
		),
	)
	if err != nil {
		res = resource.Default()
	}

	shutdownFuncs := make([]func(context.Context) error, 0, 2)

	shutdown := func(shutdownCtx context.Context) error {
		var shutdownErr error

		for _, fn := range shutdownFuncs {
			shutdownErr = errors.Join(shutdownErr, fn(shutdownCtx))
		}

		return shutdownErr
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, mp.Shutdown)

	// Register globally. If an agent/other component already set providers this is
	// tolerated by the API (it logs and keeps the existing one) so startup never fails.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return shutdown, nil
}
