// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2024
// Licensed under the MIT License.
//
// OpenTelemetry SDK bootstrap — builds and globally registers the
// TracerProvider and MeterProvider with OTLP gRPC exporters.
// Endpoint is read from OTEL_EXPORTER_OTLP_ENDPOINT (standard OTel env var; no default — set it in your deployment).
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOTel initialises the OTel SDK and returns a shutdown function.
// It must be called once at the very start of main(), before any instrumented
// code runs, and the returned shutdown must be deferred.
func initOTel(ctx context.Context) (func(), error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
		resource.WithProcess(),
		resource.WithOS(),
	)
	if err != nil {
		return func() {}, fmt.Errorf("otel resource: %w", err)
	}

	// ── Trace exporter ────────────────────────────────────────────────────────
	// Endpoint is controlled by OTEL_EXPORTER_OTLP_ENDPOINT (or
	// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT). No hardcoded default.
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return func() {}, fmt.Errorf("otel trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	// ── Metric exporter ───────────────────────────────────────────────────────
	// Endpoint is controlled by OTEL_EXPORTER_OTLP_ENDPOINT (or
	// OTEL_EXPORTER_OTLP_METRICS_ENDPOINT). No hardcoded default.
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		// Shut down the already-started trace provider before returning
		_ = tp.Shutdown(ctx)
		return func() {}, fmt.Errorf("otel metric exporter: %w", err)
	}

	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExp, metric.WithInterval(15*time.Second))),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	log.Printf("### 📡 OTel: SDK initialised (service=%s version=%s)", serviceName, version)

	shutdown := func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := tp.Shutdown(shutCtx); err != nil {
			log.Printf("### OTel: trace provider shutdown error: %v", err)
		}

		if err := mp.Shutdown(shutCtx); err != nil {
			log.Printf("### OTel: meter provider shutdown error: %v", err)
		}
	}

	return shutdown, nil
}
