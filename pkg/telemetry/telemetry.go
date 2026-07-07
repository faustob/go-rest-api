// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap and shared instrument definitions.
// Call InitSDK() once from main() before any instrumented code runs.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const scopeName = "github.com/benc-uk/go-rest-api"

// P99BudgetSeconds is the P99 latency SLO budget (750 ms). Handlers that
// exceed this add a span event to aid triage.
const P99BudgetSeconds = 0.750

// Tracer is the application-wide tracer; use it in handlers to create spans.
var Tracer trace.Tracer

// activeRequestsCount is the atomic counter backing the active-requests gauge.
var activeRequestsCount int64

// ---- Metric instruments (initialised in InitSDK) -------------------------

// FlowOutcomeCounter counts terminal flow outcomes labelled by outcome and route.
var FlowOutcomeCounter metric.Int64Counter

// FlowEntryCounter counts every time a flow entry point is invoked.
var FlowEntryCounter metric.Int64Counter

// FlowDurationHistogram records wall-clock entry-to-terminal duration in seconds.
var FlowDurationHistogram metric.Float64Histogram

// ValidationOutcomeCounter counts per-step validation outcomes.
var ValidationOutcomeCounter metric.Int64Counter

// TenantRequestCounter counts requests broken out by tenant (X-Tenant-ID header).
var TenantRequestCounter metric.Int64Counter

// ActiveRequestsUpDown tracks in-flight HTTP requests (UpDownCounter).
var ActiveRequestsUpDown metric.Int64UpDownCounter

// ---- Attribute helpers ---------------------------------------------------

// AttrFlowRoute returns a low-cardinality route attribute for flow metrics.
func AttrFlowRoute(route string) attribute.KeyValue {
	return attribute.String("http.route", route)
}

// AttrTenant extracts the X-Tenant-ID header (falls back to "unknown").
func AttrTenant(r *http.Request) attribute.KeyValue {
	tenant := r.Header.Get("X-Tenant-ID")
	if tenant == "" {
		tenant = "unknown"
	}
	return attribute.String("tenant.id", tenant)
}

// ---- SDK bootstrap -------------------------------------------------------

// InitSDK builds and globally registers the TracerProvider and MeterProvider.
// It returns a shutdown function that must be deferred by the caller.
// The OTLP endpoint is read from OTEL_EXPORTER_OTLP_ENDPOINT (default: localhost:4317).
func InitSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:4317"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	// --- Trace exporter & provider ---
	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// --- Metric exporter & provider ---
	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("otel metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second),
		)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// Initialise shared instruments.
	if initErr := initInstruments(); initErr != nil {
		log.Printf("### ⚠️  OTel: instrument init error: %v", initErr)
	}

	// Expose the shared tracer.
	Tracer = otel.Tracer(scopeName)

	shutdown := func(sCtx context.Context) error {
		var tErr, mErr error
		tErr = tp.Shutdown(sCtx)
		mErr = mp.Shutdown(sCtx)
		if tErr != nil {
			return tErr
		}
		return mErr
	}

	return shutdown, nil
}

// initInstruments creates all metric instruments against the global MeterProvider.
func initInstruments() error {
	meter := otel.Meter(scopeName)

	var err error

	FlowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal flow outcomes labelled by outcome and route."),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		return fmt.Errorf("flow.outcomes: %w", err)
	}

	FlowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Number of times a flow entry point is invoked."),
		metric.WithUnit("{invocation}"),
	)
	if err != nil {
		return fmt.Errorf("flow.entries: %w", err)
	}

	FlowDurationHistogram, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("Wall-clock entry-to-terminal flow duration."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120),
	)
	if err != nil {
		return fmt.Errorf("flow.duration: %w", err)
	}

	ValidationOutcomeCounter, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation outcomes labelled by outcome and step."),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		return fmt.Errorf("flow.validation.outcomes: %w", err)
	}

	TenantRequestCounter, err = meter.Int64Counter(
		"http.server.requests.by.tenant",
		metric.WithDescription("HTTP request count broken out by tenant and route."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("http.server.requests.by.tenant: %w", err)
	}

	ActiveRequestsUpDown, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("http.server.active_requests: %w", err)
	}

	return nil
}

// RegisterSaturationCallback registers observable gauges for active-requests
// and worker-pool size so saturation can be computed as a ratio.
// serverPort is used to derive a stable pool-size label; the actual pool size
// is read from the GOMAXPROCS-equivalent env var HTTP_WORKER_POOL_SIZE (default 100).
func RegisterSaturationCallback(serverPort int) error {
	meter := otel.Meter(scopeName)

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size."),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return fmt.Errorf("http.server.worker_pool.size: %w", err)
	}

	activeGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests.gauge",
		metric.WithDescription("Snapshot of in-flight HTTP requests (observable gauge)."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("http.server.active_requests.gauge: %w", err)
	}

	_, regErr := meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			poolSize := int64(getWorkerPoolSize())
			o.ObserveInt64(poolSizeGauge, poolSize,
				metric.WithAttributes(attribute.Int("server.port", serverPort)),
			)
			o.ObserveInt64(activeGauge, atomic.LoadInt64(&activeRequestsCount),
				metric.WithAttributes(attribute.Int("server.port", serverPort)),
			)
			return nil
		},
		poolSizeGauge, activeGauge,
	)

	return regErr
}

// getWorkerPoolSize reads HTTP_WORKER_POOL_SIZE from the environment (default 100).
func getWorkerPoolSize() int {
	val := os.Getenv("HTTP_WORKER_POOL_SIZE")
	if val == "" {
		return 100
	}
	n := 0
	for _, c := range val {
		if c < '0' || c > '9' {
			return 100
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return 100
	}
	return n
}

// ActiveRequestsMiddleware is a net/http middleware that tracks in-flight
// requests via the ActiveRequestsUpDown counter AND adds a span event when
// the handler duration exceeds the P99 budget (750 ms).
func ActiveRequestsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ActiveRequestsUpDown != nil {
			ActiveRequestsUpDown.Add(r.Context(), 1)
			atomic.AddInt64(&activeRequestsCount, 1)
			defer func() {
				ActiveRequestsUpDown.Add(r.Context(), -1)
				atomic.AddInt64(&activeRequestsCount, -1)
			}()
		}

		start := time.Now()
		next.ServeHTTP(w, r)
		elapsed := time.Since(start).Seconds()

		// P99 slow-request span event: add a diagnostic event when the handler
		// exceeds the 750 ms budget so triage can see the breakdown.
		if elapsed > P99BudgetSeconds {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow.request.p99_budget_exceeded",
				trace.WithAttributes(
					attribute.Float64("handler.duration_s", elapsed),
					attribute.Float64("p99.budget_s", P99BudgetSeconds),
					attribute.String("http.route", r.URL.Path),
				),
			)
		}
	})
}
