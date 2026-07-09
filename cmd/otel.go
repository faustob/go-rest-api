// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// OpenTelemetry SDK bootstrap: builds and registers the global TracerProvider
// and MeterProvider. Guards against double-registration if an external agent
// or another init path has already installed a global provider.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0" //nolint:depguard // versioned semconv subpackage of go.opentelemetry.io/otel
)

// setupOTelSDK builds the OpenTelemetry SDK (traces + metrics), registers it
// globally, and returns a shutdown function that flushes buffered telemetry.
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
		return shutdown, fmt.Errorf("failed to merge resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)

	// Guard against a global tracer provider already being registered
	// (e.g. by an external wrapper) — do not panic, just log and keep going.
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "### ⚠️  otel: tracer provider already set globally: %v\n", r)
			}
		}()
		otel.SetTracerProvider(tracerProvider)
	}()

	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)

	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "### ⚠️  otel: meter provider already set globally: %v\n", r)
			}
		}()
		otel.SetMeterProvider(meterProvider)
	}()

	return shutdown, nil
}
