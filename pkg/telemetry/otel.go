// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// OpenTelemetry SDK bootstrap - builds and registers TracerProvider &
// MeterProvider as global providers. This is the app-sdk wiring model for
// Go: there is no bytecode agent, so the application manages the SDK.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// SetupOTelSDK builds the OTel TracerProvider, registers it as the global
// provider, and returns a shutdown function that should be deferred by the
// caller so buffered spans are flushed on exit.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to merge resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	// Guard against a second global registration (e.g. if something else
	// already set a provider) - this should not happen for Go since there is
	// no agent, but defend anyway so startup never panics.
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("### ⚠️  Recovered while registering global OTel providers: %v\n", r)
		}
	}()

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	return func(shutdownCtx context.Context) error {
		var errs error
		if shutdownErr := tracerProvider.Shutdown(shutdownCtx); shutdownErr != nil {
			errs = errors.Join(errs, shutdownErr)
		}
		if shutdownErr := meterProvider.Shutdown(shutdownCtx); shutdownErr != nil {
			errs = errors.Join(errs, shutdownErr)
		}
		return errs
	}, nil
}
