// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap — registers global TracerProvider and
// MeterProvider backed by OTLP gRPC exporters.
//
// Configuration is entirely env-driven:
//   OTEL_SERVICE_NAME          (default: "go-rest-api")
//   OTEL_EXPORTER_OTLP_ENDPOINT (default: "localhost:4317")
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// activeRequestCount is an atomic counter used by the active-requests gauge.
var activeRequestCount int64

// initOTel builds and globally registers the TracerProvider and MeterProvider.
// It returns a shutdown function that must be deferred by the caller.
func initOTel(ctx context.Context) (func(), error) {
	svcName := os.Getenv("OTEL_SERVICE_NAME")
	if svcName == "" {
		svcName = "go-rest-api"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(svcName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	// ── Trace exporter ────────────────────────────────────────────────────────
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
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("otlp metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second),
		)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// ── Business / saturation metrics ─────────────────────────────────────────
	if err := registerSaturationMetrics(mp); err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, fmt.Errorf("saturation metrics: %w", err)
	}

	if err := registerAuthMetrics(mp); err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, fmt.Errorf("auth metrics: %w", err)
	}

	if err := registerFlowMetrics(mp); err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, fmt.Errorf("flow metrics: %w", err)
	}

	shutdown := func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = tp.Shutdown(shutCtx)
		_ = mp.Shutdown(shutCtx)
	}
	return shutdown, nil
}

// registerSaturationMetrics registers the http.server.active_requests and
// http.server.worker_pool.size observable gauges (saturation SLI).
func registerSaturationMetrics(mp *sdkmetric.MeterProvider) error {
	meter := mp.Meter("go-rest-api")

	activeReqs, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	// Worker pool size: Go's net/http uses a goroutine-per-connection model;
	// we expose the configured GOMAXPROCS value as a proxy for pool size.
	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size (GOMAXPROCS)"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(activeReqs, atomic.LoadInt64(&activeRequestCount))
			o.ObserveInt64(poolSizeGauge, int64(gomaxprocsValue()))
			return nil
		},
		activeReqs, poolSizeGauge,
	)
	return err
}

// authAttempts is the counter for authentication attempt outcomes.
var authAttempts metric.Int64Counter

// registerAuthMetrics registers the auth.attempts counter (auth failure-rate SLI).
func registerAuthMetrics(mp *sdkmetric.MeterProvider) error {
	meter := mp.Meter("go-rest-api")
	var err error
	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total authentication attempts, labelled by outcome"),
		metric.WithUnit("{attempt}"),
	)
	return err
}

// flowOutcomes is the counter for E2E business flow outcomes.
var flowOutcomes metric.Int64Counter

// flowDuration is the histogram for E2E business flow latency.
var flowDuration metric.Float64Histogram

// flowValidationOutcomes is the counter for per-step validation outcomes.
var flowValidationOutcomes metric.Int64Counter

// registerFlowMetrics registers flow-level counters and histograms.
func registerFlowMetrics(mp *sdkmetric.MeterProvider) error {
	meter := mp.Meter("go-rest-api")
	var err error

	flowOutcomes, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary E2E business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowDuration, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	flowValidationOutcomes, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcomes of per-step request validation"),
		metric.WithUnit("{validation}"),
	)
	return err
}

// gomaxprocsValue returns the current GOMAXPROCS setting.
func gomaxprocsValue() int {
	import_runtime_once.Do(func() {})
	return runtimeGOMAXPROCS(0)
}
