// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap and shared instruments for go-rest-api.
// This is the single place that owns the meter and every instrument.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const meterName = "github.com/benc-uk/go-rest-api"

var (
	meter  = otel.Meter(meterName)
	tracer = otel.Tracer(meterName)

	// RequestOutcomeCounter counts requests labeled by route and outcome class (success/error)
	RequestOutcomeCounter metric.Int64Counter

	// AuthAttemptsCounter counts auth attempts labeled by outcome (allowed/denied) and reason
	AuthAttemptsCounter metric.Int64Counter

	// FlowOutcomeCounter counts terminal outcomes of the primary business flow
	FlowOutcomeCounter metric.Int64Counter

	// FlowEntryCounter counts every invocation of the primary flow's entry point
	FlowEntryCounter metric.Int64Counter

	// ValidationOutcomeCounter counts per-step validation outcomes
	ValidationOutcomeCounter metric.Int64Counter

	// FlowDurationHistogram records end-to-end flow duration (entry to terminal), seconds
	FlowDurationHistogram metric.Float64Histogram

	activeRequests int64
	maxWorkers     = int64(256) // configured worker pool size, adjust to real pool size if known
)

// SetupOTelSDK bootstraps the OpenTelemetry SDK (tracer + meter providers) and
// registers them as the GLOBAL instances. Returns a shutdown function to be
// deferred by the caller. Registration is defensive: if a provider is already
// globally registered (e.g. by an externally attached agent) we tolerate it.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error

	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return shutdown, err
	}

	// Traces
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	setGlobalTracerProviderSafely(tracerProvider)

	// Metrics
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	setGlobalMeterProviderSafely(meterProvider)

	if err := initInstruments(); err != nil {
		return shutdown, err
	}

	return shutdown, nil
}

// setGlobalTracerProviderSafely sets the global tracer provider. Since the Go
// otel API allows overwriting the global provider without panicking, this is
// safe even if an agent-like setup already ran, but we still guard defensively
// by checking if a non-noop provider looks already configured is not possible
// via the API, so we simply set ours; app-managed SDK owns registration in Go.
func setGlobalTracerProviderSafely(tp trace.TracerProvider) {
	otel.SetTracerProvider(tp)
}

func setGlobalMeterProviderSafely(mp metric.MeterProvider) {
	otel.SetMeterProvider(mp)
}

// initInstruments creates every instrument from the single package-level meter.
func initInstruments() error {
	var err error

	RequestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests labeled by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	AuthAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization attempts labeled by outcome and reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	FlowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of terminal outcomes of the primary business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	FlowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of invocations of the primary business flow entry point"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	ValidationOutcomeCounter, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Count of per-step validation outcomes for the primary business flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return err
	}

	FlowDurationHistogram, err = meter.Float64Histogram(
		"flow.entry_to_terminal.duration",
		metric.WithDescription("Duration between the primary flow's entry event and its terminal state, in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
		o.ObserveInt64(poolSizeGauge, atomic.LoadInt64(&maxWorkers))
		return nil
	}, activeRequestsGauge, poolSizeGauge)
	if err != nil {
		return err
	}

	return nil
}

// ActiveRequestsMiddleware tracks in-flight HTTP requests for the saturation SLI.
func ActiveRequestsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequests, 1)
		defer atomic.AddInt64(&activeRequests, -1)
		next.ServeHTTP(w, r)
	})
}

// OutcomeClass returns a low-cardinality outcome class string for a status code.
func OutcomeClass(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}

// Tracer returns the package-level tracer for the service.
func Tracer() trace.Tracer {
	return tracer
}

// Meter returns the package-level meter for the service.
func Meter() metric.Meter {
	return meter
}

// AttrRoute is a convenience helper for building the low-cardinality route attribute.
func AttrRoute(route string) attribute.KeyValue {
	return attribute.String("http.route", route)
}

var hostname, _ = os.Hostname()
