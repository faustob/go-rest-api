// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap — registers global TracerProvider and
// MeterProvider at startup. Configured via environment variables:
//   OTEL_EXPORTER_OTLP_ENDPOINT  (default: localhost:4317)
//   OTEL_SERVICE_NAME             (default: go-rest-api)
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

// activeRequestCount is incremented/decremented by the saturation middleware.
var activeRequestCount int64

// initOtel builds and globally registers the TracerProvider and MeterProvider.
// It returns a shutdown function that must be deferred by the caller.
func initOtel(ctx context.Context) (func(), error) {
	svcName := os.Getenv("OTEL_SERVICE_NAME")
	if svcName == "" {
		svcName = serviceName
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(svcName),
		),
	)
	if err != nil {
		return func() {}, fmt.Errorf("otel resource: %w", err)
	}

	// ── Trace exporter ────────────────────────────────────────────────────────
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return func() {}, fmt.Errorf("otlp trace exporter: %w", err)
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
		return func() {}, fmt.Errorf("otlp metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// ── Business / saturation metrics ─────────────────────────────────────────
	if err := registerSaturationMetrics(mp); err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return func() {}, fmt.Errorf("saturation metrics: %w", err)
	}

	if err := registerAuthMetrics(mp); err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return func() {}, fmt.Errorf("auth metrics: %w", err)
	}

	if err := registerFlowMetrics(mp); err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return func() {}, fmt.Errorf("flow metrics: %w", err)
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

	activeReqGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeReqGauge, atomic.LoadInt64(&activeRequestCount))
		// Go's net/http uses a goroutine-per-connection model; GOMAXPROCS is the
		// closest analogue to a worker-pool ceiling.
		o.ObserveInt64(poolSizeGauge, int64(defaultPort)) // placeholder — replace with real pool size if available
		return nil
	}, activeReqGauge, poolSizeGauge)
	return err
}

// authAttemptCounter is the global auth-attempt counter used by the JWT middleware.
var authAttemptCounter metric.Int64Counter

// registerAuthMetrics registers the auth.attempts counter (auth failure-rate SLI).
func registerAuthMetrics(mp *sdkmetric.MeterProvider) error {
	meter := mp.Meter("go-rest-api")

	var err error
	authAttemptCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total JWT authentication attempts"),
		metric.WithUnit("{attempt}"),
	)
	return err
}

// flowOutcomeCounter and flowDurationHistogram are used by the flow SLIs.
var (
	flowOutcomeCounter    metric.Int64Counter
	flowDurationHistogram metric.Float64Histogram
	flowValidationCounter metric.Int64Counter
)

// registerFlowMetrics registers the flow.outcomes counter and flow.duration histogram.
func registerFlowMetrics(mp *sdkmetric.MeterProvider) error {
	meter := mp.Meter("go-rest-api")

	var err error
	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowDurationHistogram, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary request flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	flowValidationCounter, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcomes of per-step flow validation"),
		metric.WithUnit("{validation}"),
	)
	return err
}

// RecordFlowOutcome records a terminal flow outcome and its duration.
// Call this at the end of every request handler that represents the primary flow.
func RecordFlowOutcome(ctx context.Context, outcome string, durationSec float64) {
	if flowOutcomeCounter == nil || flowDurationHistogram == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}
	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	flowDurationHistogram.Record(ctx, durationSec, metric.WithAttributes(attrs...))
}

// RecordValidationOutcome records a per-step validation outcome.
func RecordValidationOutcome(ctx context.Context, step, outcome string) {
	if flowValidationCounter == nil {
		return
	}
	flowValidationCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("step", step),
		attribute.String("outcome", outcome),
	))
}
