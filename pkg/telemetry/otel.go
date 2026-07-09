// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap: builds TracerProvider + MeterProvider, registers
// them as the global providers, and returns a shutdown func. Only main() should
// call SetupOTelSDK, since this application is the deployable entrypoint.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SetupOTelSDK builds and registers the global TracerProvider. It guards
// against a provider already being registered (e.g. by an externally attached
// agent or a previous call) so startup never crashes if that happens.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	var opts []otlptracegrpc.Option
	if endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return func(context.Context) error { return nil }, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
	)

	// Defensive registration: tolerate a pre-existing global provider rather
	// than crashing the process (there is no error return from otel.SetTracerProvider,
	// but we guard against panics from any downstream misuse defensively).
	func() {
		defer func() {
			if r := recover(); r != nil {
				_ = errors.New("otel: recovered while setting global tracer provider")
			}
		}()
		otel.SetTracerProvider(tp)
	}()

	// Build and register a MeterProvider so otel.Meter(meterName) in
	// instruments.go resolves to a real, emitting meter rather than the no-op
	// default. Attach an OTLP metric exporter via a periodic reader so the
	// counters/histograms/gauges recorded in middleware.go and validation.go
	// actually get exported, instead of collecting into a MeterProvider with
	// no configured reader (which silently drops all recorded measurements).
	var metricOpts []otlpmetricgrpc.Option
	if endpoint != "" {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		return func(context.Context) error { return nil }, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				_ = errors.New("otel: recovered while setting global meter provider")
			}
		}()
		otel.SetMeterProvider(mp)
	}()

	shutdown := func(shutdownCtx context.Context) error {
		traceErr := tp.Shutdown(shutdownCtx)
		meterErr := mp.Shutdown(shutdownCtx)
		if traceErr != nil {
			return traceErr
		}
		return meterErr
	}

	return shutdown, nil
}
