// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap & shared instruments for the API
//
// The SDK is registered globally ONCE from main() via InitOTel; every other
// package simply uses the global providers through the helpers below.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"log"

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

// ScopeName is the single instrumentation scope used across the service.
const ScopeName = "github.com/benc-uk/go-rest-api"

// ONE meter and ONE tracer per service — every instrument below is created from these.
var (
	meter  = otel.Meter(ScopeName)
	tracer = otel.Tracer(ScopeName)
)

// Instruments. The inbound request duration histogram is emitted by otelhttp
// (http.server.request.duration, seconds); these are the additional SLI signals.
var (
	// requestCount backs the availability, 5xx error-rate and throughput SLIs.
	requestCount metric.Int64Counter

	// authAttempts backs the authentication failure-rate SLI.
	authAttempts metric.Int64Counter

	// activeRequests is a value that goes up AND down -> UpDownCounter.
	activeRequests metric.Int64UpDownCounter
)

func init() {
	var err error

	requestCount, err = meter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### 📉 Telemetry: failed to create http.server.request.count counter: %s", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome and denial reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### 📉 Telemetry: failed to create auth.attempts counter: %s", err)
	}

	activeRequests, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### 📉 Telemetry: failed to create http.server.active_requests counter: %s", err)
	}
}

// Tracer returns the shared tracer for this service.
func Tracer() trace.Tracer {
	return tracer
}

// RecordAuthOutcome records a single authentication/authorization decision.
// outcome is "allowed" or "denied"; reason is a low-cardinality denial class.
func RecordAuthOutcome(ctx context.Context, outcome string, reason string) {
	if authAttempts == nil {
		return
	}

	attrs := []attribute.KeyValue{attribute.String("outcome", outcome)}
	if reason != "" {
		attrs = append(attrs,
			attribute.String("auth.deny_reason", reason),
			attribute.String("error.type", reason),
		)
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// InitOTel builds the OpenTelemetry SDK and registers it as the GLOBAL provider.
// The OTLP endpoint is env driven (OTEL_EXPORTER_OTLP_ENDPOINT). It is safe to
// call when an external collector/agent is unavailable: errors are logged and a
// no-op shutdown is returned so the application always starts.
func InitOTel(ctx context.Context, serviceName string, serviceVersion string) func(context.Context) error {
	noop := func(context.Context) error { return nil }

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		log.Printf("### 📉 Telemetry: resource error, using default: %s", err)

		res = resource.Default()
	}

	// Context propagation, so spans nest across services
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		log.Printf("### 📉 Telemetry: OTLP trace exporter disabled: %s", err)
		return noop
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		log.Printf("### 📉 Telemetry: OTLP metric exporter disabled: %s", err)

		return tp.Shutdown
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	log.Printf("### 📡 Telemetry: OpenTelemetry SDK registered for service '%s'", serviceName)

	return func(shutdownCtx context.Context) error {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			log.Printf("### 📉 Telemetry: tracer provider shutdown: %s", err)
		}

		return mp.Shutdown(shutdownCtx)
	}
}
