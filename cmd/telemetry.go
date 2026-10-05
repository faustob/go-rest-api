// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap: builds and registers the TracerProvider and
// MeterProvider used by every SLI in this service, and declares the shared
// custom instruments recorded by the telemetry middleware in
// telemetry_middleware.go.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// meter is the single, package-level meter for this service. Every
// instrument MUST be created from this meter, never a new one obtained
// elsewhere, so metrics are never double-counted or split across providers.
var meter = otel.Meter("go-rest-api")

var (
	// httpServerDurationHistogram is the OTel semantic-convention HTTP server
	// request duration histogram (seconds). Used to derive availability,
	// latency (p95/p99), error-rate and throughput SLIs.
	httpServerDurationHistogram metric.Float64Histogram

	// httpRequestOutcomeCounter counts requests by route and outcome class
	// (success / client_error / error) for the availability SLI.
	httpRequestOutcomeCounter metric.Int64Counter

	// httpRequestsByTenantCounter breaks out request throughput by
	// tenant/API key for the per-tenant throughput SLI.
	httpRequestsByTenantCounter metric.Int64Counter

	// authAttemptCounter counts every authentication/authorization decision,
	// tagged by outcome and denial reason, for the auth-failure-rate SLI.
	authAttemptCounter metric.Int64Counter
)

// initInstruments creates all custom instruments for this service from the
// single package-level meter. Must be called once, after the MeterProvider
// has been registered globally.
func initInstruments() error {
	var err error

	httpServerDurationHistogram, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	httpRequestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	httpRequestsByTenantCounter, err = meter.Int64Counter(
		"http.server.requests.by_tenant",
		metric.WithDescription("Count of HTTP requests by route and tenant/API key"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	authAttemptCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	return nil
}

// initTelemetry builds the OpenTelemetry TracerProvider and MeterProvider
// for this service, exporting via OTLP/gRPC (endpoint configured by the
// standard OTEL_EXPORTER_OTLP_ENDPOINT env var), and registers them as the
// process-wide global providers. It returns a shutdown function the caller
// MUST defer to flush buffered telemetry on exit.
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
		log.Printf("### ⚠️ Failed to build OpenTelemetry resource, using default: %v", err)
		res = resource.Default()
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	if err := initInstruments(); err != nil {
		log.Printf("### ⚠️ Failed to initialise OpenTelemetry instruments: %v", err)
	}

	shutdown := func(shutdownCtx context.Context) error {
		return errors.Join(
			tracerProvider.Shutdown(shutdownCtx),
			meterProvider.Shutdown(shutdownCtx),
		)
	}

	return shutdown, nil
}
