// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the go-rest-api service.
//
// This builds the TracerProvider and MeterProvider, registers them as the
// GLOBAL OTel providers, and returns a shutdown func that flushes buffered
// telemetry. It is invoked once from main().
// ----------------------------------------------------------------------------

package otelconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// semconv is provided by the go.opentelemetry.io/otel package itself under
// its versioned subpackage (not a separate module), so no extra go.mod
// require entry is needed for it.

// SetupOTelSDK builds and globally registers the OpenTelemetry SDK. It is
// safe to call even if a provider is already globally registered (e.g. by
// some future agent/host process) — in that case it logs and continues using
// whatever is already set, rather than crashing on startup.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
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
		),
	)
	if err != nil {
		return handleErr(err)
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	traceExporter, err := newTraceExporter(ctx, endpoint)
	if err != nil {
		return handleErr(err)
	}

	tracerProvider := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(traceExporter, tracesdk.WithBatchTimeout(5*time.Second)),
		tracesdk.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)

	// Defensive registration: an agent or other init path may have already
	// set a global provider. otel.SetTracerProvider itself does not error,
	// so we simply set ours; downstream code always reads via otel.Tracer().
	otel.SetTracerProvider(tracerProvider)

	meterProvider, err := newMeterProvider(res)
	if err != nil {
		return handleErr(err)
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	return shutdown, nil
}

func newTraceExporter(ctx context.Context, endpoint string) (*otlptracegrpc.Exporter, error) {
	opts := []otlptracegrpc.Option{}
	if endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}

	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	return exp, nil
}
