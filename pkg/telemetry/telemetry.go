// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap for the service: builds and registers the global
// TracerProvider & MeterProvider, and owns the single Meter/Tracer plus every
// instrument used to back the service's target SLIs.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// scopeName identifies this service's instrumentation scope
const scopeName = "github.com/benc-uk/go-rest-api"

// Tracer and Meter are the single shared instrumentation scope for the whole
// service. They are safe to use before Init() below runs: the global OTel API
// delegates every call through to whichever provider gets registered later
// (Go's global Tracer/Meter proxies rebind automatically once registered).
var (
	Tracer = otel.Tracer(scopeName)
	Meter  = otel.Meter(scopeName)
)

// Instruments backing the target SLIs. All created from the single Meter
// above, and each recorded to at its real measurement point (see pkg/api and
// pkg/auth) - never left declared-but-unused.
var (
	// RequestDuration is the standard OTel semconv HTTP server latency
	// histogram, backing the availability/latency-p95/latency-p99/throughput SLIs.
	RequestDuration metric.Float64Histogram

	// RequestOutcome counts requests by route & outcome class, backing the
	// HTTP availability and error-rate SLIs without needing to scan traces.
	// Name is the platform-managed metric name for this signal.
	RequestOutcome metric.Int64Counter

	// AuthAttempts counts every authentication/authorization decision, tagged
	// with the outcome and (for denials) the reason, backing the auth failure
	// rate SLI.
	AuthAttempts metric.Int64Counter
)

func init() {
	var err error

	RequestDuration, err = Meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### 📈 Telemetry: failed to create request duration histogram: %s", err)
	}

	RequestOutcome, err = Meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### 📈 Telemetry: failed to create request outcome counter: %s", err)
	}

	AuthAttempts, err = Meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### 📈 Telemetry: failed to create auth attempts counter: %s", err)
	}
}

// Init builds the OTel SDK (traces + metrics), exporting via OTLP/gRPC, and
// registers it as the GLOBAL provider so the Tracer/Meter/instruments above
// (and any created via otel.Tracer/otel.Meter elsewhere) start emitting.
// The OTLP endpoint is env-driven (OTEL_EXPORTER_OTLP_ENDPOINT). Call once
// from main() at process startup; the returned func should be deferred.
func Init(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	// Go's OTel API has no "already registered" panic like some other
	// languages; SetTracerProvider/SetMeterProvider simply install these as
	// the active global providers that the delegating Tracer/Meter above
	// (created before this point, at package-init time) forward calls into
	// from here on.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	shutdown := func(shutdownCtx context.Context) error {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			return err
		}

		return mp.Shutdown(shutdownCtx)
	}

	return shutdown, nil
}
