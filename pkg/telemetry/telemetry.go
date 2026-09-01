// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap: builds and registers the global TracerProvider
// and MeterProvider for this service, and declares the ONE package-level
// meter/instruments used across the app. Go has no bytecode agent, so this
// app-managed SDK is the only place providers are ever registered.
// ----------------------------------------------------------------------------

package telemetry

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
	"go.opentelemetry.io/otel/trace"
)

// Package-level meter/tracer. There is exactly ONE meter for this service;
// every instrument (and any future callback) must be created from it.
var (
	Tracer trace.Tracer
	Meter  metric.Meter

	// RequestOutcomeCounter counts HTTP requests by route + outcome class
	// (success/failure), used for the availability SLI.
	RequestOutcomeCounter metric.Int64Counter

	// RequestDurationHist is the standard OTel semconv HTTP server request
	// duration histogram, recorded in seconds.
	RequestDurationHist metric.Float64Histogram

	// AuthAttemptsCounter counts every auth decision (allowed/denied), used
	// for the authentication failure rate SLI.
	AuthAttemptsCounter metric.Int64Counter

	// TenantRequestCounter counts requests per tenant/API key, used for the
	// per-tenant throughput SLI.
	TenantRequestCounter metric.Int64Counter
)

// Init builds the OTel SDK TracerProvider and MeterProvider and registers
// them as the GLOBAL providers. It returns a shutdown function that MUST be
// deferred by the caller (main) so buffered telemetry is flushed on exit.
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to build resource: %w", err)
	}

	traceExporterOpts := []otlptracegrpc.Option{otlptracegrpc.WithInsecure()}
	if endpoint != "" {
		traceExporterOpts = append(traceExporterOpts, otlptracegrpc.WithEndpoint(endpoint))
	}
	traceExporter, err := otlptracegrpc.New(
		ctx,
		traceExporterOpts...,
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to create trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporterOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithInsecure()}
	if endpoint != "" {
		metricExporterOpts = append(metricExporterOpts, otlpmetricgrpc.WithEndpoint(endpoint))
	}
	metricExporter, err := otlpmetricgrpc.New(
		ctx,
		metricExporterOpts...,
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to create metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(res),
	)

	// Defensive registration: guard against a panic if a global provider was
	// already set (e.g. Init invoked more than once), so the app still starts.
	registerGlobalsSafely(tp, mp)

	Tracer = otel.Tracer(serviceName)
	Meter = otel.Meter(serviceName)

	if err := initInstruments(); err != nil {
		return nil, err
	}

	shutdown := func(shutdownCtx context.Context) error {
		var firstErr error
		if err := tp.Shutdown(shutdownCtx); err != nil {
			firstErr = err
		}
		if err := mp.Shutdown(shutdownCtx); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}

	return shutdown, nil
}

// registerGlobalsSafely sets the global tracer/meter providers, recovering
// from a panic so that if a global provider is somehow already registered,
// the app still starts and simply keeps using whichever provider won.
func registerGlobalsSafely(tp *sdktrace.TracerProvider, mp *sdkmetric.MeterProvider) {
	defer func() {
		_ = recover()
	}()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
}

func initInstruments() error {
	var err error

	RequestOutcomeCounter, err = Meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests by route and outcome class (success/failure)"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create request outcome counter: %w", err)
	}

	RequestDurationHist, err = Meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create request duration histogram: %w", err)
	}

	AuthAttemptsCounter, err = Meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization attempts by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create auth attempts counter: %w", err)
	}

	TenantRequestCounter, err = Meter.Int64Counter(
		"http.server.requests.by_tenant",
		metric.WithDescription("Count of HTTP requests by tenant/API key"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create tenant request counter: %w", err)
	}

	return nil
}
