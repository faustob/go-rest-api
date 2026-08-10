// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the API server.
//
// This is the deployable binary (package main), so it -- and only it -- builds
// and registers the global tracer & meter providers. Libraries under pkg/ just
// use the global providers.
//
// The OTLP endpoint is env-driven: set OTEL_EXPORTER_OTLP_ENDPOINT (and the
// usual OTEL_* variables). Nothing is hardcoded.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/benc-uk/go-rest-api/pkg/telemetry"
)

// initOTel builds the SDK and registers it globally. It returns a shutdown
// function that flushes buffered telemetry.
//
// If no OTLP endpoint is configured the SDK is left alone: the global providers
// stay as whatever is already registered (no-op, or a provider set by an
// external runtime), and the instrumented code keeps working unchanged.
func initOTel(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" &&
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" &&
		os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" {
		log.Printf("### \U0001F4E1 OTel: no OTLP endpoint configured, skipping SDK registration")

		return noop, nil
	}

	// OTEL_SERVICE_NAME wins if set; otherwise fall back to the build-time name.
	name := os.Getenv("OTEL_SERVICE_NAME")
	if name == "" {
		name = serviceName
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(name),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return noop, err
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return noop, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		// Tracing is already up; tear it down so we don't leak an exporter.
		_ = tp.Shutdown(ctx)

		return noop, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Register defensively: in Go, setting the globals again is safe (the API
	// logs rather than panics) and instruments rebind to the new provider, so
	// this works with or without an externally-configured provider.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	for _, e := range telemetry.InitErrors() {
		log.Printf("### \U0001F4E1 OTel: instrument creation error: %s", e)
	}

	log.Printf("### \U0001F4E1 OTel: SDK registered for service '%s'", name)

	return func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}, nil
}
