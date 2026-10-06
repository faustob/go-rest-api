// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap: builds and registers the global TracerProvider
// and MeterProvider for this service. Must be called once from main() before
// any instrumented code runs. The OTLP endpoint is env-driven via the
// standard OTEL_EXPORTER_OTLP_ENDPOINT variable, read by the otlp exporters.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// setupOTel builds the OpenTelemetry SDK (tracing + metrics) and registers it
// as the global provider, returning a shutdown func to flush buffered data on
// exit. Safe to call exactly once, from main().
func setupOTel(ctx context.Context) (func(context.Context) error, error) {
	svcName := serviceName
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		svcName = v
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(svcName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create otel resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	shutdown := func(shutdownCtx context.Context) error {
		sctx, cancel := context.WithTimeout(shutdownCtx, 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(sctx); err != nil {
			return err
		}
		return mp.Shutdown(sctx)
	}

	return shutdown, nil
}
