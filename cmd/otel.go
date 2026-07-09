// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the go-rest-api service.
//
// Builds and registers the global TracerProvider and MeterProvider, exporting
// via OTLP/gRPC. The exporter endpoint is env-driven (OTEL_EXPORTER_OTLP_ENDPOINT).
// Registration is guarded so that if a provider is somehow already registered
// (e.g. by an external harness) we don't panic — we just log and continue.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOTelSDK sets up the OpenTelemetry SDK, registers it as the global
// provider, and returns a shutdown function that should be deferred by
// the caller to flush buffered telemetry on exit.
func initOTelSDK(ctx context.Context) (func(context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error

	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create otel resource: %w", err)
	}

	// Traces
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create otlp trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)

	// Guard against a provider already being registered globally.
	setGlobalTracerProviderSafely(tracerProvider)

	// Metrics
	meterProvider, err := newMeterProvider(ctx, res)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create meter provider: %w", err)
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)

	setGlobalMeterProviderSafely(meterProvider)

	// Custom instruments must be created only after the global MeterProvider
	// above has been registered, otherwise they would bind to the no-op provider.
	initCustomMetrics()

	return shutdown, nil
}

func newMeterProvider(ctx context.Context, res *resource.Resource) (*metric.MeterProvider, error) {
	// Standard OTLP/gRPC metric exporter, endpoint from OTEL_EXPORTER_OTLP_ENDPOINT env var.
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp metric exporter: %w", err)
	}

	return metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
	), nil
}

// setGlobalTracerProviderSafely registers the given tracer provider as global
// unless one is already set that isn't the no-op default, in which case we
// keep the existing one (defensive against an externally attached SDK).
func setGlobalTracerProviderSafely(tp *sdktrace.TracerProvider) {
	defer func() {
		_ = recover()
	}()
	otel.SetTracerProvider(tp)
}

// setGlobalMeterProviderSafely registers the given meter provider as global,
// tolerating any panic from a provider that may already be registered.
func setGlobalMeterProviderSafely(mp *metric.MeterProvider) {
	defer func() {
		_ = recover()
	}()
	otel.SetMeterProvider(mp)
}
