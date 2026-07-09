// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap: builds and registers the TracerProvider and
// MeterProvider as global instances, wired to an OTLP/gRPC endpoint that is
// configured via the OTEL_EXPORTER_OTLP_ENDPOINT environment variable.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	metricsdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOTelSDK builds and registers the OpenTelemetry SDK providers globally,
// returning a shutdown function that must be deferred by the caller.
//
// Registration is defensive: if a provider is already registered globally
// (e.g. by an externally-attached agent or a prior call) this function will
// not panic, it simply proceeds and returns a shutdown that tears down what
// it created here.
func initOTelSDK(ctx context.Context) (func(context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error

	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	handleErr := func(inErr error) (func(context.Context) error, error) {
		return shutdown, errors.Join(inErr, shutdown(ctx))
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
		return handleErr(fmt.Errorf("failed to merge resource: %w", err))
	}

	// Trace exporter, endpoint is env-driven via OTEL_EXPORTER_OTLP_ENDPOINT
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return handleErr(fmt.Errorf("failed to create OTLP trace exporter: %w", err))
	}

	tracerProvider := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(traceExporter),
		tracesdk.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Meter provider: SDK default reader; metric export target can be wired
	// via env if a metrics exporter dependency is later added. For now the
	// SDK MeterProvider is registered globally so instruments created via
	// otel.Meter(...) are functional (in-process aggregation), and can be
	// extended with a reader/exporter without touching call sites.
	meterProvider := metricsdk.NewMeterProvider(
		metricsdk.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	return shutdown, nil
}
