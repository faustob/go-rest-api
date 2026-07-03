// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap — registers global TracerProvider and
// MeterProvider backed by OTLP gRPC exporters.
// OTEL_EXPORTER_OTLP_ENDPOINT controls the collector address.
// OTEL_SERVICE_NAME controls the service name (default: go-rest-api).
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// globalMeter is the application-wide meter used by all instrumented packages.
var globalMeter metric.Meter

// initOTel bootstraps the OpenTelemetry SDK and returns a shutdown function
// that must be deferred by the caller (main).
func initOTel(ctx context.Context) (func(context.Context) error, error) {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "go-rest-api"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithOS(),
	)
	if err != nil {
		return nil, err
	}

	// --- Trace exporter ---
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// --- Metric exporter ---
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second),
		)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	globalMeter = otel.Meter("github.com/benc-uk/go-rest-api")

	shutdown := func(ctx context.Context) error {
		if err := tp.Shutdown(ctx); err != nil {
			return err
		}
		return mp.Shutdown(ctx)
	}
	return shutdown, nil
}
