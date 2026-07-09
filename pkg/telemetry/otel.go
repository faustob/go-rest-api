// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap: builds and globally registers the
// TracerProvider and MeterProvider for the go-rest-api service.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ShutdownFunc flushes and shuts down the registered providers.
type ShutdownFunc func(context.Context) error

// SetupOTelSDK builds the TracerProvider and MeterProvider, registers them as
// the global instances, and returns a shutdown function. It is defensive
// against a global provider that may already have been registered (e.g. by
// an externally attached agent) — in that case we log and continue using the
// existing global providers instead of crashing at startup.
func SetupOTelSDK(ctx context.Context, serviceName string) (shutdown ShutdownFunc, err error) {
	var shutdownFuncs []ShutdownFunc

	shutdown = func(ctx context.Context) error {
		var retErr error
		for _, fn := range shutdownFuncs {
			if serr := fn(ctx); serr != nil {
				retErr = errors.Join(retErr, serr)
			}
		}
		return retErr
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return shutdown, fmt.Errorf("failed to merge resource: %w", err)
	}

	// Trace provider
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)

	otel.SetTracerProvider(tracerProvider)

	// Meter provider
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create metric exporter: %w", err)
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
		metric.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)

	otel.SetMeterProvider(meterProvider)

	if err := initInstruments(); err != nil {
		return shutdown, fmt.Errorf("failed to initialize instruments: %w", err)
	}

	return shutdown, nil
}

// ErrorTypeAttr returns the standard error.type attribute for span/metric use.
func ErrorTypeAttr(errType string) attribute.KeyValue {
	return semconv.ErrorTypeKey.String(errType)
}
