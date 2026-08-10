// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap & shared instruments for the go-rest-api service.
//
// A single meter and tracer are declared here and used by every instrument in
// this package. The SDK is registered globally from main() via Init().
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const scopeName = "github.com/benc-uk/go-rest-api"

// ONE meter and ONE tracer for the whole service.
var (
	meter  = otel.Meter(scopeName)
	tracer = otel.Tracer(scopeName)
)

// Shared instruments, created once at package init.
var (
	requestOutcomes  metric.Int64Counter
	requestsInFlight metric.Int64UpDownCounter
	authAttempts     metric.Int64Counter
	handlerOutcomes  metric.Int64Counter
)

func init() {
	var err error

	requestOutcomes, err = meter.Int64Counter(
		"http.server.request.outcome",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️ telemetry: failed to create http.server.request.outcome: %s", err)
	}

	requestsInFlight, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️ telemetry: failed to create http.server.active_requests: %s", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions by outcome and reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### ⚠️ telemetry: failed to create auth.attempts: %s", err)
	}

	handlerOutcomes, err = meter.Int64Counter(
		"http.server.handler.outcome",
		metric.WithDescription("Business outcome of an HTTP handler, by route template"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		log.Printf("### ⚠️ telemetry: failed to create http.server.handler.outcome: %s", err)
	}
}

// Init builds the OpenTelemetry SDK and registers it as the global provider.
// It is safe to call when an external agent/collector is not reachable; the
// returned shutdown function flushes buffered telemetry.
func Init(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		res = resource.Default()
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)

		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Register defensively - if an agent already installed providers this simply
	// overwrites the local no-op default; otel.Set* never panics.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// Re-bind the package meter/tracer now that a real SDK is registered.
	meter = otel.Meter(scopeName)
	tracer = otel.Tracer(scopeName)

	return func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}, nil
}

// Tracer exposes the single service tracer.
func Tracer() trace.Tracer {
	return tracer
}

// RecordHandlerOutcome records a business outcome for a handler, keyed by the
// low-cardinality route template, and mirrors the outcome class onto the span.
func RecordHandlerOutcome(ctx context.Context, route, outcome string) {
	if handlerOutcomes != nil {
		handlerOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
		))
	}

	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(attribute.String("app.handler.outcome", outcome))
	}
}

// slowRequestBudget is the P99 latency objective; exceeding it emits a span event for triage.
const slowRequestBudget = 750 * time.Millisecond
