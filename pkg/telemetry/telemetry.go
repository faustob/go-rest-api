// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap & shared instruments for the go-rest-api module
//
// InitOTel is called ONCE from the application entrypoint (cmd/server.go main())
// and registers the SDK as the global provider. Library code in this module only
// uses the global provider via the single package level meter below.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
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
)

// ScopeName is the single instrumentation scope used across this module
const ScopeName = "github.com/benc-uk/go-rest-api"

// ONE meter for the whole service, every instrument is created from it
var (
	meter        = otel.Meter(ScopeName)
	authAttempts metric.Int64Counter
)

func init() {
	var err error

	authAttempts, err = meter.Int64Counter("auth.attempts",
		metric.WithDescription("Authentication & authorization decisions, by outcome and denial reason"),
		metric.WithUnit("{attempt}"))
	if err != nil {
		log.Printf("### 📡 OTel: failed to create auth.attempts counter: %s", err)
	}
}

// RecordAuthAttempt records a single authentication/authorization decision.
// outcome is "allowed" or "denied", reason is a low cardinality denial class (may be empty)
func RecordAuthAttempt(ctx context.Context, outcome string, reason string) {
	if authAttempts == nil {
		return
	}

	attrs := []attribute.KeyValue{attribute.String("auth.outcome", outcome)}
	if reason != "" {
		// A denial is a normal authorization outcome, not an error class, so it gets
		// its own dedicated low cardinality attribute rather than semconv error.type
		attrs = append(attrs, attribute.String("auth.denial_reason", reason))
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// InitOTel builds the OpenTelemetry SDK and registers it as the GLOBAL provider.
// Exporter configuration is env driven, e.g. OTEL_EXPORTER_OTLP_ENDPOINT.
// The returned function flushes & shuts down the providers, it is never nil.
func InitOTel(ctx context.Context, serviceName string, serviceVersion string) (func(context.Context) error, error) {
	noopShutdown := func(context.Context) error { return nil }

	res := resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	)

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return noopShutdown, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return tracerProvider.Shutdown, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	log.Printf("### 📡 OTel: SDK registered globally for service '%s'", serviceName)

	return func(shutdownCtx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(shutdownCtx), meterProvider.Shutdown(shutdownCtx))
	}, nil
}
