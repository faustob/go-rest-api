// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2025
// Licensed under the MIT License.
//
// OpenTelemetry SDK bootstrap and shared instruments for the service.
// This is the ONLY place that builds/registers the global TracerProvider and
// MeterProvider, and the ONLY place that creates instruments - every other
// package records to the instruments exposed here via the Record*/helper funcs.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/benc-uk/go-rest-api/pkg/env"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// tracer & meter are the single shared instances for this service.
// Every instrument below is created from this same meter - obtaining it via
// otel.Meter/otel.Tracer means it automatically rebinds once Init() registers
// the real providers (Go's global otel package forwards to whatever provider
// is currently registered).
var (
	tracer = otel.Tracer("go-rest-api")
	meter  = otel.Meter("go-rest-api")
)

// Instruments, all created once from the shared meter above
var (
	httpServerRequestDuration metric.Float64Histogram
	authAttempts              metric.Int64Counter
	flowEntries               metric.Int64Counter
	flowOutcomes              metric.Int64Counter
	flowDuration              metric.Float64Histogram
	validationOutcomes        metric.Int64Counter
)

// inFlightRequests backs the http.server.active_requests observable gauge
var inFlightRequests int64

// workerPoolSize is the configured logical capacity used for the saturation SLI
var workerPoolSize = env.GetEnvInt("HTTP_WORKER_POOL_SIZE", 250)

func init() {
	var err error

	httpServerRequestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create http.server.request.duration: %s", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create auth.attempts: %s", err)
	}

	flowEntries, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of primary request-flow entry invocations"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create flow.entries: %s", err)
	}

	flowOutcomes, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of primary request-flow terminal outcomes"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create flow.outcomes: %s", err)
	}

	flowDuration, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithUnit("s"),
		metric.WithDescription("End-to-end duration of the primary request flow, entry to terminal state"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create flow.duration: %s", err)
	}

	validationOutcomes, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Count of request-flow validation step outcomes"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create flow.validation.outcomes: %s", err)
	}

	activeRequests, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create http.server.active_requests: %s", err)
	}

	poolSize, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured logical worker pool size for the HTTP server"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create http.server.worker_pool.size: %s", err)
	}

	if activeRequests != nil && poolSize != nil {
		_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(activeRequests, atomic.LoadInt64(&inFlightRequests))
			o.ObserveInt64(poolSize, int64(workerPoolSize))
			return nil
		}, activeRequests, poolSize)
		if err != nil {
			log.Printf("### 📊 Telemetry: failed to register saturation callback: %s", err)
		}
	}
}

// Init builds and registers the global OTel TracerProvider & MeterProvider, exporting via
// OTLP/gRPC. The endpoint is entirely env-driven: if OTEL_EXPORTER_OTLP_ENDPOINT is unset we
// deliberately do NOT set a WithEndpoint option, so the exporters fall back to their own
// standard OTEL_EXPORTER_OTLP_* env resolution (and their own built-in default) rather than a
// value hardcoded here. Returns a shutdown func that must be deferred by main().
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	endpoint := env.GetEnvString("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	insecure := env.GetEnvBool("OTEL_EXPORTER_OTLP_INSECURE", true)

	var traceOpts []otlptracegrpc.Option

	var metricOpts []otlpmetricgrpc.Option

	// Only override the endpoint when explicitly configured - otherwise let the
	// exporters resolve it themselves from the standard OTel env vars/defaults
	if endpoint != "" {
		traceOpts = append(traceOpts, otlptracegrpc.WithEndpoint(endpoint))
		metricOpts = append(metricOpts, otlpmetricgrpc.WithEndpoint(endpoint))
	}

	if insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	}

	traceExporter, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	metricExporter, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	logEndpoint := endpoint
	if logEndpoint == "" {
		logEndpoint = "(default OTLP resolution)"
	}

	log.Printf("### 🔭 Telemetry: OpenTelemetry SDK initialized, exporting OTLP/gRPC to: %s", logEndpoint)

	return func(shutdownCtx context.Context) error {
		if shutdownErr := tp.Shutdown(shutdownCtx); shutdownErr != nil {
			return shutdownErr
		}

		return mp.Shutdown(shutdownCtx)
	}, nil
}

// Tracer returns the single shared tracer for this service
func Tracer() trace.Tracer {
	return tracer
}

// StartInFlight increments the in-flight request gauge and returns a func to decrement it,
// intended to be called with `defer` at the point a request begins being handled
func StartInFlight() func() {
	atomic.AddInt64(&inFlightRequests, 1)

	return func() {
		atomic.AddInt64(&inFlightRequests, -1)
	}
}

// RecordHTTPServerRequest records the standard OTel http.server.request.duration histogram
func RecordHTTPServerRequest(ctx context.Context, method, scheme, route string, statusCode int, tenantTier string, duration time.Duration, errType string) {
	if httpServerRequestDuration == nil {
		return
	}

	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(method),
		semconv.URLSchemeKey.String(scheme),
	}

	if route != "" {
		attrs = append(attrs, semconv.HTTPRouteKey.String(route))
	}

	if statusCode > 0 {
		attrs = append(attrs, semconv.HTTPResponseStatusCodeKey.Int(statusCode))
	}

	if errType != "" {
		attrs = append(attrs, semconv.ErrorTypeKey.String(errType))
	}

	if tenantTier != "" {
		attrs = append(attrs, attribute.String("tenant.tier", tenantTier))
	}

	httpServerRequestDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}

// RecordAuthAttempt records an authentication/authorization decision, tagged with the reason for denial
func RecordAuthAttempt(ctx context.Context, outcome, reason string) {
	if authAttempts == nil {
		return
	}

	attrs := []attribute.KeyValue{attribute.String("outcome", outcome)}
	if reason != "" {
		attrs = append(attrs, attribute.String("reason", reason))
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordFlowEntry increments the primary business-flow entry counter, independent of outcome
func RecordFlowEntry(ctx context.Context) {
	if flowEntries == nil {
		return
	}

	flowEntries.Add(ctx, 1)
}

// RecordFlowOutcome records the terminal outcome and entry-to-terminal duration of the primary flow
func RecordFlowOutcome(ctx context.Context, outcome string, duration time.Duration) {
	attrs := metric.WithAttributes(attribute.String("outcome", outcome))

	if flowOutcomes != nil {
		flowOutcomes.Add(ctx, 1, attrs)
	}

	if flowDuration != nil {
		flowDuration.Record(ctx, duration.Seconds(), attrs)
	}
}

// RecordValidationOutcome records the pass/fail outcome of a single named validation step in the flow
func RecordValidationOutcome(ctx context.Context, step, outcome string) {
	if validationOutcomes == nil {
		return
	}

	validationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("step", step),
		attribute.String("outcome", outcome),
	))
}
