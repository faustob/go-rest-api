// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for the API server binary.
//
// This is the ONLY place the SDK is built and registered globally. The pkg/*
// libraries never register a provider, they simply read the global one.
//
// Configuration is environment driven, per the OTel spec, e.g:
//   OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4317
//   OTEL_SERVICE_NAME=my-service
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOtel builds the trace & metric providers and registers them as the global
// OpenTelemetry instances. It returns a shutdown function that flushes buffered
// telemetry; the returned function is always safe to call.
//
// If the exporters cannot be created (e.g. no collector configured) we log and
// carry on with whatever provider is already installed - startup never fails
// because of telemetry, and an externally installed provider is left alone.
func initOtel(ctx context.Context, serviceName string, serviceVersion string) func(context.Context) error {
	noop := func(context.Context) error { return nil }

	// Resource: service.name/service.version are the required resource attributes.
	// OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES still take precedence via
	// the SDK env detectors merged in by resource.Default().
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		log.Printf("### 📡 OTel: could not build resource: %s", err)

		res = resource.Default()
	}

	// Endpoint & protocol come from OTEL_EXPORTER_OTLP_ENDPOINT et al.
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		log.Printf("### 📡 OTel: trace exporter disabled: %s", err)

		return noop
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		log.Printf("### 📡 OTel: metric exporter disabled: %s", err)

		return func(shutdownCtx context.Context) error { return traceExp.Shutdown(shutdownCtx) }
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Register globally so otel.Meter/otel.Tracer in the pkg/ libraries resolve
	// to real implementations instead of no-ops.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("### 📡 OTel: SDK registered for service '%s'", serviceName)

	return func(shutdownCtx context.Context) error {
		err := tp.Shutdown(shutdownCtx)
		if mErr := mp.Shutdown(shutdownCtx); err == nil {
			err = mErr
		}

		return err
	}
}
