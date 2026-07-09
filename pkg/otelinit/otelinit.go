// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// otelinit builds and registers the OpenTelemetry TracerProvider and
// MeterProvider as the GLOBAL instances for the application. This is an
// application entrypoint concern (app-managed SDK, no agent for Go), so it
// must be invoked once from main().
// ----------------------------------------------------------------------------

package otelinit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ShutdownFunc flushes and shuts down the registered providers.
type ShutdownFunc func(ctx context.Context) error

// Init builds the TracerProvider and MeterProvider, exporting via OTLP/gRPC,
// and registers them as the global providers. It is safe to call even when
// an OTel provider may already be globally registered (defensive registration).
func Init(ctx context.Context, serviceName string) (ShutdownFunc, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:4317"
	}

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

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create metric exporter: %w", err)
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter, metric.WithInterval(15*time.Second))),
		metric.WithResource(res),
	)

	// Defensive registration: an agent or previous init may have already set a
	// global provider. otel.SetTracerProvider/SetMeterProvider do not error on
	// re-registration in the Go SDK, but we guard for future-proofing anyway.
	func() {
		defer func() {
			if r := recover(); r != nil {
				_ = errors.New("recovered while setting global tracer provider")
			}
		}()
		otel.SetTracerProvider(tracerProvider)
	}()

	func() {
		defer func() {
			if r := recover(); r != nil {
				_ = errors.New("recovered while setting global meter provider")
			}
		}()
		otel.SetMeterProvider(meterProvider)
	}()

	shutdown := func(ctx context.Context) error {
		var errs []error
		if err := tracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		if err := meterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}

	return shutdown, nil
}
