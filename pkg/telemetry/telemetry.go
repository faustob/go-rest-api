// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap for go-rest-api
//
// Builds and registers the global TracerProvider & MeterProvider, and owns
// every metric instrument for the service. All instruments MUST be created
// here, from the single shared meter, and used by other packages via the
// exported vars/functions below.
//
// Init MUST be called, and its error MUST be treated as fatal, before the
// HTTP router (and RequestTelemetryMiddleware) is constructed - otherwise
// Tracer/instruments below would still be their zero value (nil) when the
// middleware tries to record against them.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/benc-uk/go-rest-api/pkg/env"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Tracer is the single shared tracer for the service.
// Meter-derived instruments below are the only metric instruments the service creates.
var (
	Tracer trace.Tracer
	meter  metric.Meter

	// HTTPServerDuration is the OTel semantic-convention inbound HTTP request duration histogram (seconds).
	HTTPServerDuration metric.Float64Histogram

	// AuthAttempts counts every JWT auth decision, tagged by outcome (allowed/denied).
	// Named under the http.server domain to keep the new metric corpus in one consistent namespace.
	AuthAttempts metric.Int64Counter

	// FlowOutcomes counts the terminal outcome of the primary request flow.
	FlowOutcomes metric.Int64Counter

	// FlowDuration is the end-to-end duration of the primary request flow (seconds).
	FlowDuration metric.Float64Histogram

	// FlowEntryToTerminalDuration is the wall-clock time between flow entry and its terminal state (seconds).
	FlowEntryToTerminalDuration metric.Float64Histogram

	// FlowEntries counts every invocation of the primary flow's entry point, independent of outcome.
	FlowEntries metric.Int64Counter

	// ValidationOutcomes counts the outcome of each request validation step (e.g. JWT auth).
	ValidationOutcomes metric.Int64Counter

	activeRequests int64
)

// IncActiveRequests increments the in-flight HTTP request count
func IncActiveRequests() {
	atomic.AddInt64(&activeRequests, 1)
}

// DecActiveRequests decrements the in-flight HTTP request count
func DecActiveRequests() {
	atomic.AddInt64(&activeRequests, -1)
}

// Init builds the OpenTelemetry SDK (traces + metrics), registers it as the global
// SDK, and creates every instrument used by the service. The OTLP gRPC endpoint is
// entirely env-driven (OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_*_ENDPOINT).
// Returns a shutdown function that must be deferred by the caller to flush telemetry.
//
// Callers MUST treat a non-nil error as fatal and stop startup: Tracer and every
// instrument var above are left as their zero value (nil) on error, and recording
// against a nil instrument panics.
func Init(ctx context.Context, serviceName string, serviceVersion string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to build resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to create trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: failed to create metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	// Register as global providers. Go has no bytecode agent that could have
	// pre-registered a provider, so a plain Set is safe here.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	Tracer = tp.Tracer(serviceName)
	meter = mp.Meter(serviceName)

	if err := createInstruments(); err != nil {
		return nil, err
	}

	shutdown := func(shutdownCtx context.Context) error {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			return err
		}

		return mp.Shutdown(shutdownCtx)
	}

	return shutdown, nil
}

// createInstruments creates every metric instrument used by the service, all from the
// single shared meter above, and registers the observable-gauge callback.
func createInstruments() error {
	var err error

	HTTPServerDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create http.server.request.duration: %w", err)
	}

	AuthAttempts, err = meter.Int64Counter(
		"http.server.auth.attempts",
		metric.WithDescription("Count of JWT authentication/authorization decisions"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create http.server.auth.attempts: %w", err)
	}

	FlowOutcomes, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of the primary business flow, one per completed request"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create flow.outcomes: %w", err)
	}

	FlowDuration, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End to end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create flow.duration: %w", err)
	}

	FlowEntryToTerminalDuration, err = meter.Float64Histogram(
		"flow.entry_to_terminal.duration",
		metric.WithDescription("Wall clock time between flow entry and its terminal state"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create flow.entry_to_terminal.duration: %w", err)
	}

	FlowEntries, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of primary business flow entry invocations, independent of outcome"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create flow.entries: %w", err)
	}

	ValidationOutcomes, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcome of each request validation step, e.g. JWT auth"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create flow.validation.outcomes: %w", err)
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create http.server.active_requests: %w", err)
	}

	workerPoolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured maximum number of concurrent request handlers"),
	)
	if err != nil {
		return fmt.Errorf("telemetry: failed to create http.server.worker_pool.size: %w", err)
	}

	poolSize := int64(env.GetEnvInt("WORKER_POOL_SIZE", 100))

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
		o.ObserveInt64(workerPoolSizeGauge, poolSize)

		return nil
	}, activeRequestsGauge, workerPoolSizeGauge)
	if err != nil {
		return fmt.Errorf("telemetry: failed to register saturation callback: %w", err)
	}

	log.Println("### 📈 Telemetry: OpenTelemetry instruments registered")

	return nil
}
