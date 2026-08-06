// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the API server.
//
// This is the APPLICATION entrypoint, so it is the only place that builds and
// registers the global OTel providers. Library packages (pkg/...) only read the
// global providers.
//
// Exporter configuration is environment driven, e.g.
//   OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_PROTOCOL, OTEL_SERVICE_NAME
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOTel builds the OTel SDK and registers it as the global provider.
// It returns a shutdown function that flushes buffered telemetry.
//
// It is deliberately forgiving: if exporter setup fails (no collector
// configured, bad endpoint, ...) the app still starts and the instrumentation
// simply falls back to the no-op / already-registered providers.
func initOTel(svcName string, svcVersion string) func() {
	ctx := context.Background()
	noop := func() {}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(svcName),
			semconv.ServiceVersion(svcVersion),
		),
	)
	if err != nil {
		log.Printf("### 📡 OTel: resource error, using default resource: %s", err)

		res = resource.Default()
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		log.Printf("### 📡 OTel: trace exporter disabled: %s", err)

		return noop
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		log.Printf("### 📡 OTel: metric exporter disabled: %s", err)

		return noop
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Registering is safe to repeat in Go (it overwrites rather than panics),
	// but keep it in one place so an externally injected provider is respected
	// only when we could not build our own (handled by the early returns above).
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("### 📡 OTel: SDK registered for service '%s'", svcName)

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := tp.Shutdown(shutdownCtx); err != nil {
			log.Printf("### 📡 OTel: tracer provider shutdown error: %s", err)
		}

		if err := mp.Shutdown(shutdownCtx); err != nil {
			log.Printf("### 📡 OTel: meter provider shutdown error: %s", err)
		}
	}
}
