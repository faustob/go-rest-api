// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// OpenTelemetry SDK bootstrap: builds and registers the global TracerProvider
// and MeterProvider. This is the application entrypoint (main package), so it
// owns SDK lifecycle per the app-sdk wiring model for Go.
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

// setupOTelSDK bootstraps the OpenTelemetry SDK and registers it globally.
// It returns a shutdown function that flushes buffered telemetry and should
// be deferred by the caller.
func setupOTelSDK(ctx context.Context) (func(context.Context) error, error) {
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
		return shutdown, fmt.Errorf("failed to merge otel resource: %w", err)
	}

	// Trace exporter, endpoint driven by OTEL_EXPORTER_OTLP_ENDPOINT env var.
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create otlp trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)

	otel.SetTracerProvider(tracerProvider)

	if err := setupMeterProvider(res, &shutdownFuncs); err != nil {
		return shutdown, err
	}

	return shutdown, nil
}

// setupMeterProvider builds an OTLP-exporting MeterProvider and registers it
// as the global meter provider. Shutdown functions are appended to
// shutdownFuncs so the caller's deferred shutdown flushes buffered metrics.
func setupMeterProvider(res *resource.Resource, shutdownFuncs *[]func(context.Context) error) error {
	ctx := context.Background()

	// Metric exporter, endpoint driven by OTEL_EXPORTER_OTLP_ENDPOINT env var.
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return fmt.Errorf("failed to create otlp metric exporter: %w", err)
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
		metric.WithResource(res),
	)
	*shutdownFuncs = append(*shutdownFuncs, meterProvider.Shutdown)

	otel.SetMeterProvider(meterProvider)

	return nil
}
