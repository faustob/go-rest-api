// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap: builds and registers the global TracerProvider and
// MeterProvider (OTLP/gRPC, endpoint driven by OTEL_EXPORTER_OTLP_ENDPOINT),
// and declares this service's OTel instruments from a single package-level
// Meter, per the one-meter-per-service convention.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// instrumentationScope names the single package-level Meter/Tracer used for
// EVERY instrument and span in this service - never call otel.Meter/otel.Tracer
// anywhere else in this codebase.
const instrumentationScope = "github.com/benc-uk/go-rest-api"

var meter = otel.Meter(instrumentationScope)

// Tracer is used to add span events (e.g. slow-request events) to the active server span.
var Tracer = otel.Tracer(instrumentationScope)

// Instruments - ALL created from the single package-level meter above.
var (
	// HTTPServerDuration is the OTel semconv histogram for inbound HTTP request duration, in seconds.
	HTTPServerDuration metric.Float64Histogram

	// RequestOutcomeCounter counts inbound HTTP requests labeled by route, method, outcome class and tenant.
	RequestOutcomeCounter metric.Int64Counter

	// AuthAttemptsCounter counts authentication/authorization decisions labeled by outcome and reason.
	AuthAttemptsCounter metric.Int64Counter
)

func init() {
	var err error

	HTTPServerDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(fmt.Errorf("telemetry: failed to create http.server.request.duration histogram: %w", err))
	}

	RequestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of inbound HTTP requests labeled by route, method, outcome class and tenant"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(fmt.Errorf("telemetry: failed to create http.server.request.outcomes counter: %w", err))
	}

	AuthAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions labeled by outcome and reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		panic(fmt.Errorf("telemetry: failed to create auth.attempts counter: %w", err))
	}
}

// InitOTel builds the TracerProvider and MeterProvider, exporting via
// OTLP/gRPC (endpoint driven by the standard OTEL_EXPORTER_OTLP_ENDPOINT env
// var), and registers them as the global providers. Callers MUST defer the
// returned shutdown function so buffered telemetry is flushed on exit.
func InitOTel(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to build resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to create OTLP trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to create OTLP metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

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
