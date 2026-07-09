// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap for go-rest-api
// Builds and registers the global TracerProvider and MeterProvider.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

var (
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
)

// SetupOTelSDK builds and registers the global OTel tracer/meter providers.
// It returns a shutdown function that should be deferred by the caller.
// Registration is defensive: if a global provider is already set (e.g. by an
// agent) we still build our own local providers to use, but we avoid panics.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	var shutdownFuncs []func(context.Context) error

	traceExporterOpts := []otlptracegrpc.Option{}
	if endpoint != "" {
		traceExporterOpts = append(traceExporterOpts, otlptracegrpc.WithEndpointURL(endpoint))
	}

	traceExporter, err := otlptracegrpc.New(ctx, traceExporterOpts...)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	tracerProvider = tp
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)

	metricExporterOpts := []otlpmetricgrpc.Option{}
	if endpoint != "" {
		metricExporterOpts = append(metricExporterOpts, otlpmetricgrpc.WithEndpointURL(endpoint))
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, metricExporterOpts...)
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(15*time.Second))),
	)
	meterProvider = mp
	shutdownFuncs = append(shutdownFuncs, mp.Shutdown)

	// Defensive global registration: recover from a panic if a global provider
	// is already installed by an agent or previous init, and keep using our
	// locally-built providers regardless.
	setGlobalSafely(func() { otel.SetTracerProvider(tp) })
	setGlobalSafely(func() { otel.SetMeterProvider(mp) })

	if err := initInstruments(mp); err != nil {
		return nil, err
	}

	shutdown := func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	return shutdown, nil
}

func setGlobalSafely(fn func()) {
	defer func() {
		_ = recover()
	}()
	fn()
}

// TracerProvider returns the locally-built tracer provider, falling back to
// the global one if setup hasn't run (e.g. in tests).
func TracerProvider() trace.TracerProvider {
	if tracerProvider != nil {
		return tracerProvider
	}
	return otel.GetTracerProvider()
}

// MeterProvider returns the locally-built meter provider, falling back to
// the global one if setup hasn't run (e.g. in tests).
func MeterProvider() metric.MeterProvider {
	if meterProvider != nil {
		return meterProvider
	}
	return otel.GetMeterProvider()
}
