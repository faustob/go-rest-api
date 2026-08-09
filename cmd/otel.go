// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the sample API server.
//
// Called from main() in cmd/server.go, this builds the tracer & meter providers
// and registers them as the GLOBAL OpenTelemetry providers so the instrumentation
// in pkg/telemetry is not a no-op. Exporter configuration is env driven, e.g.
// OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_SERVICE_NAME.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initTelemetry builds the OTel SDK, registers it globally and returns a
// shutdown function which flushes buffered spans & metrics.
func initTelemetry(ctx context.Context, name string, ver string) (func(context.Context) error, error) {
	resAttrs := []attribute.KeyValue{semconv.ServiceVersion(ver)}

	// Let OTEL_SERVICE_NAME win when it is provided by the deployment
	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		resAttrs = append(resAttrs, semconv.ServiceName(name))
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, resAttrs...))
	if err != nil {
		res = resource.Default()
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		// Stop the trace pipeline we already started before giving up
		_ = tracerProvider.Shutdown(context.Background())

		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	// Global registration is tolerant in Go: if something (e.g. an operator or
	// another bootstrap) already set a provider, OTel logs a warning and keeps
	// working rather than panicking, so this is safe either way.
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(shutdownCtx context.Context) error {
		return errors.Join(
			tracerProvider.Shutdown(shutdownCtx),
			meterProvider.Shutdown(shutdownCtx),
		)
	}, nil
}
