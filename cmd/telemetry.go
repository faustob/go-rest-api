// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap for go-rest-api.
//
// This is the application entrypoint (cmd/main), so it — and only it — owns
// SDK construction/registration. Go has no bytecode agent, so the app always
// manages the SDK (app-sdk wiring model). Packages under pkg/ must only
// consume the already-registered global providers via otel.Tracer/otel.Meter.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// otelMeter is a delegating handle bound to whatever MeterProvider is
// currently registered globally (safe to use even before initTelemetry runs).
// One meter for the whole service — every instrument below is created from it.
var otelMeter = otel.Meter(serviceName)

// activeRequests tracks in-flight HTTP requests for the saturation gauge.
var activeRequests int64

// SLI instruments, created once here and recorded from cmd/otel_middleware.go.
var (
	httpServerDuration  metric.Float64Histogram
	httpRequestOutcomes metric.Int64Counter
	authAttempts        metric.Int64Counter
	flowEntries         metric.Int64Counter
	flowOutcomes        metric.Int64Counter
	flowDuration        metric.Float64Histogram
	activeRequestsGauge metric.Int64ObservableGauge
)

func init() {
	var err error

	httpServerDuration, err = otelMeter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create http.server.request.duration: %v", err)
	}

	httpRequestOutcomes, err = otelMeter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("Count of HTTP requests by route, tenant, and outcome class (success, client_error, error)"),
		metric.WithUnit("1"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create http.server.request.count: %v", err)
	}

	authAttempts, err = otelMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
		metric.WithUnit("1"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create auth.attempts: %v", err)
	}

	flowEntries, err = otelMeter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of entries into the primary request flow"),
		metric.WithUnit("1"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create flow.entries: %v", err)
	}

	flowOutcomes, err = otelMeter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of the primary request flow"),
		metric.WithUnit("1"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create flow.outcomes: %v", err)
	}

	flowDuration, err = otelMeter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary request flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create flow.duration: %v", err)
	}

	activeRequestsGauge, err = otelMeter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("1"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create http.server.active_requests: %v", err)
	}

	if activeRequestsGauge != nil {
		_, err = otelMeter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
			return nil
		}, activeRequestsGauge)
		if err != nil {
			log.Printf("### ⚠️ otel: failed to register active-requests callback: %v", err)
		}
	}
}

// initTelemetry builds and registers the global TracerProvider and
// MeterProvider, exporting via OTLP/gRPC. The endpoint is env-driven via the
// standard OTEL_EXPORTER_OTLP_ENDPOINT (and related) environment variables.
// It returns a shutdown function that flushes buffered telemetry.
func initTelemetry(ctx context.Context) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel: failed to build resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel: failed to create trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	// SetTracerProvider never panics on re-registration; it simply replaces the
	// global — safe to call unconditionally even if something else set one.
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel: failed to create metric exporter: %w", err)
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	return func(shutdownCtx context.Context) error {
		if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("otel: tracer provider shutdown: %w", err)
		}
		if err := meterProvider.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("otel: meter provider shutdown: %w", err)
		}
		return nil
	}, nil
}
