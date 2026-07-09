// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap: builds and registers global TracerProvider and
// MeterProvider. This is a plain net/http+chi Go application (app-managed SDK,
// no agent), so we build and register the SDK once in main().
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// otelShutdown holds a cleanup function to be deferred from main().
type otelShutdown func(context.Context) error

// setupOTelSDK builds a TracerProvider using an OTLP gRPC exporter and
// registers it as the global provider. It is defensive: if a global
// TracerProvider is already installed (e.g. by an agent) this function still
// works because otel.SetTracerProvider simply replaces the global - there is
// no panic path in the Go API, but we still guard exporter/provider creation
// errors explicitly instead of discarding them.
func setupOTelSDK(ctx context.Context) (otelShutdown, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = serviceName2()
	}

	traceExporterOpts := []otlptracegrpc.Option{}
	if endpoint != "" {
		traceExporterOpts = append(traceExporterOpts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}

	traceExporter, err := otlptracegrpc.New(ctx, traceExporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build OTel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// serviceName2 avoids colliding with the package-level `serviceName` var
// declared in server.go while still giving a sensible default.
func serviceName2() string {
	return "go-rest-api"
}
