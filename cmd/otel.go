// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap — registers global TracerProvider and
// MeterProvider backed by OTLP gRPC exporters.
// Endpoint is driven by OTEL_EXPORTER_OTLP_ENDPOINT (default: localhost:4317).
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOTel builds and globally registers the TracerProvider and MeterProvider.
// It returns a shutdown function that must be deferred by the caller.
// The OTLP endpoint is read from OTEL_EXPORTER_OTLP_ENDPOINT (default: localhost:4317
// when unset, which is the SDK default — no hardcoded override here).
func initOTel(ctx context.Context) (func(), error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String(version),
		),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	// ── Trace exporter ────────────────────────────────────────────────────────
	// otlptracegrpc.New reads OTEL_EXPORTER_OTLP_ENDPOINT (or
	// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT) from the environment automatically.
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	// ── Metric exporter ───────────────────────────────────────────────────────
	// otlpmetricgrpc.New reads OTEL_EXPORTER_OTLP_ENDPOINT (or
	// OTEL_EXPORTER_OTLP_METRICS_ENDPOINT) from the environment automatically.
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("otlp metric exporter: %w", err)
	}

	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExp, metric.WithInterval(15*time.Second))),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	shutdown := func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = tp.Shutdown(shutCtx)
		_ = mp.Shutdown(shutCtx)
	}
	return shutdown, nil
}
