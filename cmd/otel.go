// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap — registers global TracerProvider and
// MeterProvider at process startup. Called from main() before any handler
// is registered.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// otelInstruments holds every metric instrument used across the service.
type otelInstruments struct {
	// http.server.request.duration — OTel semantic convention histogram (seconds)
	// emitted by otelhttp middleware; also used for flow duration.
	flowDuration metric.Float64Histogram

	// flow.outcomes — terminal outcome counter for E2E business flow
	flowOutcomes metric.Int64Counter

	// flow.entry — entry-point counter (throughput)
	flowEntry metric.Int64Counter

	// flow.validation.outcomes — per-step validation outcome counter
	flowValidationOutcomes metric.Int64Counter

	// auth.attempts — authentication decision counter
	authAttempts metric.Int64Counter

	// http.server.active_requests — in-flight request gauge (UpDownCounter)
	activeRequests metric.Int64UpDownCounter
}

// globalInstruments is populated by initOTel and used by handlers.
var globalInstruments *otelInstruments

// initOTel builds and registers the global TracerProvider and MeterProvider.
// It returns a shutdown function that must be deferred in main().
func initOTel(ctx context.Context) (func(context.Context) error, error) {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "go-rest-api"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
		resource.WithProcess(),
		resource.WithOS(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	// --- Trace exporter ---
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

	// --- Metric exporter ---
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// --- Build instruments ---
	meter := otel.Meter("go-rest-api")

	flowDuration, err := meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end business flow duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("flow.duration histogram: %w", err)
	}

	flowOutcomes, err := meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of the primary business flow"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("flow.outcomes counter: %w", err)
	}

	flowEntry, err := meter.Int64Counter(
		"flow.entry",
		metric.WithDescription("Number of times the primary flow entry point is invoked"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("flow.entry counter: %w", err)
	}

	flowValidationOutcomes, err := meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation outcome for the primary flow"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("flow.validation.outcomes counter: %w", err)
	}

	authAttempts, err := meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, fmt.Errorf("auth.attempts counter: %w", err)
	}

	activeRequests, err := meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("http.server.active_requests updowncounter: %w", err)
	}

	globalInstruments = &otelInstruments{
		flowDuration:           flowDuration,
		flowOutcomes:           flowOutcomes,
		flowEntry:              flowEntry,
		flowValidationOutcomes: flowValidationOutcomes,
		authAttempts:           authAttempts,
		activeRequests:         activeRequests,
	}

	shutdown := func(ctx context.Context) error {
		var firstErr error
		if err := tp.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := mp.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	return shutdown, nil
}
