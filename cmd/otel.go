// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap — initialises TracerProvider and MeterProvider,
// registers them as globals, and returns a shutdown function.
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
// The returned function must be deferred by the caller to flush and shut down.
// The OTLP endpoint is read from OTEL_EXPORTER_OTLP_ENDPOINT (default: localhost:4317
// when the env var is absent — the gRPC exporter's built-in default).
func initOTel(ctx context.Context) (func(), error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String(version),
		),
		resource.WithProcess(),
		resource.WithOS(),
	)
	if err != nil {
		return nil, fmt.Errorf("build OTel resource: %w", err)
	}

	// ── Trace exporter ────────────────────────────────────────────────────────
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	// ── Metric exporter ───────────────────────────────────────────────────────
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
	}

	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExp,
			metric.WithInterval(15*time.Second),
		)),
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
