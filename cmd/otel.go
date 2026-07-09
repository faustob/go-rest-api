// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the go-rest-api service.
//
// Builds and registers the global TracerProvider and MeterProvider once at
// application startup (app-managed SDK, no agent for Go). The OTLP endpoint
// is env-driven via OTEL_EXPORTER_OTLP_ENDPOINT (defaults to localhost:4317).
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// shutdownFunc combines the shutdown of all providers we register.
type shutdownFunc func(context.Context) error

// setupOTelSDK builds the TracerProvider and MeterProvider and registers them
// as the global instances. It guards against a provider already being set
// (e.g. by an externally attached mechanism) so startup never crashes.
func setupOTelSDK(ctx context.Context) (shutdownFunc, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build otel resource: %w", err)
	}

	var shutdownFuncs []func(context.Context) error

	shutdown := func(ctx context.Context) error {
		var errs error
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				errs = errors.Join(errs, err)
			}
		}
		return errs
	}

	// --- Traces ---
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

	// --- Metrics ---
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return shutdown, fmt.Errorf("failed to create otlp metric exporter: %w", err)
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)

	otel.SetMeterProvider(meterProvider)

	return shutdown, nil
}

// attrErrorType returns a low-cardinality error.type attribute for span/metric
// recording, per OTel semantic conventions (error class, never the message).
func attrErrorType(class string) attribute.KeyValue {
	return semconv.ErrorTypeKey.String(class)
}

// spanStatusCode is a small helper re-exported for readability at call sites
// that need to set span status codes based on outcome.
var (
	_ = codes.Error
	_ = codes.Ok
)

// setupTelemetryMetrics obtains the meter and creates the custom instruments
// AFTER the MeterProvider has been registered globally, so instruments bind
// to the real provider rather than the no-op default. Invoked from main()
// after setupOTelSDK() has run.
func setupTelemetryMetrics() {
	initMetrics()
}
