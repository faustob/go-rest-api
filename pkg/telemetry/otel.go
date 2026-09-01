// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap and shared instruments for go-rest-api.
// This is the ONLY place the TracerProvider/MeterProvider are built and
// registered globally; everything else obtains the global tracer/meter.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
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

const scopeName = "github.com/benc-uk/go-rest-api"

// Tracer and meter are the shared, service-wide instrumentation scope
// handles. They are safe to obtain at package-init time: the global otel
// API returns delegating implementations that forward to whatever real
// provider Init registers later.
var (
	Tracer = otel.Tracer(scopeName)
	meter  = otel.Meter(scopeName)
)

// Instruments - each defined ONCE, from the single package-level meter above.
var (
	HTTPRequestDuration metric.Float64Histogram
	HTTPRequestCounter  metric.Int64Counter
	AuthAttemptsCounter metric.Int64Counter
)

func init() {
	var err error

	HTTPRequestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		otel.Handle(fmt.Errorf("failed to create http.server.request.duration histogram: %w", err))
	}

	HTTPRequestCounter, err = meter.Int64Counter(
		"http.server.requests",
		metric.WithDescription("Count of inbound HTTP requests, labeled by route, outcome and tenant"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		otel.Handle(fmt.Errorf("failed to create http.server.requests counter: %w", err))
	}

	AuthAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions, labeled by outcome and reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		otel.Handle(fmt.Errorf("failed to create auth.attempts counter: %w", err))
	}
}

// Init builds and registers the OpenTelemetry TracerProvider and
// MeterProvider as the GLOBAL providers for the process. It returns a
// shutdown function that the caller (main) MUST defer, to flush buffered
// telemetry on exit. Exporter endpoints are env-driven via the standard
// OTEL_EXPORTER_OTLP_ENDPOINT (and related) environment variables consumed
// by otlptracegrpc/otlpmetricgrpc.
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
		return nil, fmt.Errorf("failed to build otel resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp metric exporter: %w", err)
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	// Go's otel API does not panic on a second Set*, so this is safe to call
	// even if something else in the process already registered providers.
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	shutdown := func(shutdownCtx context.Context) error {
		var errs []error
		if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
		if err := meterProvider.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}

	return shutdown, nil
}
