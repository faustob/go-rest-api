// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap, this is the APPLICATION entrypoint so it is the
// only place that builds & registers the global providers.
//
// Configuration is environment driven, e.g:
//   OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_PROTOCOL, OTEL_SERVICE_NAME
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	stdlog "log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// initOpenTelemetry builds the trace & metric providers and registers them as global.
// It returns a shutdown function that flushes buffered telemetry.
func initOpenTelemetry(ctx context.Context, svcName string, svcVersion string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(svcName),
			semconv.ServiceVersion(svcVersion),
		),
	)
	if err != nil {
		return nil, err
	}

	// Endpoint & protocol come from the OTEL_EXPORTER_OTLP_* environment variables
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		_ = traceExp.Shutdown(ctx)
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Errors from the SDK are logged and never fatal, so telemetry can't take the app down
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		stdlog.Printf("### \U0001F4E1 OTel: %s", err)
	}))

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	stdlog.Printf("### \U0001F4E1 OTel: telemetry enabled for service '%s'", svcName)

	shutdown := func(shutdownCtx context.Context) error {
		return errors.Join(
			tracerProvider.Shutdown(shutdownCtx),
			meterProvider.Shutdown(shutdownCtx),
		)
	}

	return shutdown, nil
}
