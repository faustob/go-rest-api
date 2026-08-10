// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the APPLICATION entrypoint (cmd/server.go).
// Library packages must NOT call this - they use the global providers.
//
// The OTLP endpoint is env-driven: OTEL_EXPORTER_OTLP_ENDPOINT (and the usual
// OTEL_* env vars). Nothing is hardcoded.
// ----------------------------------------------------------------------------

package telemetry

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
)

// InitSDK builds and globally registers the OpenTelemetry tracer & meter providers.
// It returns a shutdown function that flushes buffered telemetry.
//
// If an OTLP endpoint is not configured, telemetry export is skipped and a no-op
// shutdown is returned - the instrumented code paths are unaffected either way.
func InitSDK(ctx context.Context, serviceName string, serviceVersion string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" &&
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" &&
		os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" {
		log.Printf("### OTel: no OTEL_EXPORTER_OTLP_ENDPOINT set, skipping SDK setup")

		return noop, nil
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	))
	if err != nil {
		return noop, err
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return noop, err
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return func(shutdownCtx context.Context) error { return traceExp.Shutdown(shutdownCtx) }, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Register defensively: if something else (a wrapper or sidecar bootstrap)
	// already registered providers, we log and continue rather than crash.
	setGlobals(tp, mp)

	shutdown := func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}

	log.Printf("### OTel: SDK registered for service %s", serviceName)

	return shutdown, nil
}

// setGlobals registers the providers globally, tolerating any panic from an
// already-configured global (so the app always starts).
func setGlobals(tp *sdktrace.TracerProvider, mp *sdkmetric.MeterProvider) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("### OTel: global providers already set, continuing (%v)", r)
		}
	}()

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}
