// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap and shared instruments for the sample API server.
//
// This file owns THE single meter/tracer for the service; all instruments are
// created from them. It registers the SDK as the global provider at startup,
// tolerating the case where an external agent already registered one.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const telemetryScope = "github.com/benc-uk/go-rest-api"

// The ONE meter and tracer for this service, all instruments come from these.
var (
	meter  = otel.Meter(telemetryScope)
	tracer = otel.Tracer(telemetryScope)
)

// Shared instruments, created once in initInstruments().
var (
	requestOutcomeCounter metric.Int64Counter
	requestDuration       metric.Float64Histogram
	authAttemptsCounter   metric.Int64Counter
)

// initInstruments creates every instrument from the single service meter.
func initInstruments() error {
	var err error

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcome",
		metric.WithDescription("Count of inbound HTTP requests by route, status and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	authAttemptsCounter, err = meter.Int64Counter(
		"http.server.auth.decisions",
		metric.WithDescription("Count of authentication/authorization decisions by outcome and denial reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	return nil
}

// initTelemetry builds the OTel SDK, registers it globally (defensively) and
// returns a shutdown func that flushes buffered spans.
func initTelemetry(ctx context.Context, svcName, svcVersion string) (func(context.Context) error, error) {
	if envName := os.Getenv("OTEL_SERVICE_NAME"); envName != "" {
		svcName = envName
	}

	shutdown := func(context.Context) error { return nil }

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(svcName),
		semconv.ServiceVersion(svcVersion),
	))
	if err != nil {
		res = resource.Default()
	}

	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		// No exporter, still create instruments so recording is a safe no-op.
		if instErr := initInstruments(); instErr != nil {
			return shutdown, instErr
		}

		return shutdown, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	// Metric pipeline: OTLP exporter behind a periodic reader, so recorded
	// measurements actually leave the process.
	var mp *sdkmetric.MeterProvider

	metricExporter, metricErr := otlpmetricgrpc.New(ctx)
	if metricErr != nil {
		log.Printf("### ⚠️ OpenTelemetry metric exporter init failed: %s", metricErr)
	} else {
		mp = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
			sdkmetric.WithResource(res),
		)
	}

	// Register defensively: if an agent already set a global provider, keep it.
	registerGlobals(tp, mp)

	// Re-resolve meter/tracer from whichever provider is now global.
	meter = otel.Meter(telemetryScope)
	tracer = otel.Tracer(telemetryScope)

	shutdown = func(shutdownCtx context.Context) error {
		err := tp.Shutdown(shutdownCtx)

		if mp != nil {
			if mErr := mp.Shutdown(shutdownCtx); mErr != nil && err == nil {
				err = mErr
			}
		}

		return err
	}

	if err := initInstruments(); err != nil {
		return shutdown, err
	}

	return shutdown, nil
}

// registerGlobals sets the global tracer provider & propagators, recovering if
// an already-installed agent/SDK causes a panic on set.
func registerGlobals(tp trace.TracerProvider, mp *sdkmetric.MeterProvider) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("### ⚠️ OpenTelemetry global already registered, using existing provider: %v", r)
		}
	}()

	otel.SetTracerProvider(tp)

	if mp != nil {
		otel.SetMeterProvider(mp)
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
}

// statusClass maps a status code to a low cardinality outcome class.
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// tenantAttr derives a LOW cardinality business/tenant dimension from headers.
func tenantAttr(tier string) attribute.KeyValue {
	if tier == "" {
		tier = "unknown"
	}

	return attribute.String("tenant.tier", tier)
}
