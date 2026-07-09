// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap and instrumentation helpers for go-rest-api
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

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
	otrace "go.opentelemetry.io/otel/trace"

	"github.com/go-chi/chi/v5"
)

const serviceScope = "go-rest-api"

// meter is the single package-level meter used by every instrument in this service.
var meter = otel.Meter(serviceScope)

// tracer is the single package-level tracer used for manual spans/events.
var tracer = otel.Tracer(serviceScope)

var (
	requestOutcomeCounter    metric.Int64Counter
	authAttemptCounter       metric.Int64Counter
	flowOutcomeCounter       metric.Int64Counter
	flowEntryCounter         metric.Int64Counter
	flowDurationHistogram    metric.Float64Histogram
	validationOutcomeCounter metric.Int64Counter

	activeRequests atomic.Int64
	maxWorkers     int64 = 100 // configured worker pool size (adjust as needed)
)

func init() {
	var err error

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(err)
	}

	authAttemptCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization attempts by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		panic(err)
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of end-to-end business flow terminal outcomes"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		panic(err)
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of entries into the primary business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		panic(err)
	}

	flowDurationHistogram, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(err)
	}

	validationOutcomeCounter, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Count of per-step validation outcomes within the primary flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		panic(err)
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(err)
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured size of the HTTP worker pool"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		panic(err)
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, activeRequests.Load())
		o.ObserveInt64(poolSizeGauge, maxWorkers)
		return nil
	}, activeRequestsGauge, poolSizeGauge)
	if err != nil {
		panic(err)
	}
}

// SetupOTelSDK builds and registers the global TracerProvider and MeterProvider.
// It returns a shutdown function that should be deferred by the caller.
// Registration is defensive: if a global provider is already set (e.g. by an
// externally attached agent) this still proceeds since Go has no bytecode agent,
// but we guard exporter creation errors instead of panicking the whole app.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error

	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithHost(),
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
	otel.SetTracerProvider(tracerProvider)

	// Metrics
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	return shutdown, nil
}

// RouteTemplate returns the matched chi route pattern (low-cardinality) for
// the given request, falling back to "unknown" if no route matched yet.
func RouteTemplate(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return "unknown"
	}
	pattern := rctx.RoutePattern()
	if pattern == "" {
		return "unknown"
	}
	return pattern
}

// ErrorTypeAttr returns a standard error.type attribute for span annotation.
func ErrorTypeAttr(errType string) attribute.KeyValue {
	return attribute.String("error.type", errType)
}

// statusRecorder wraps http.ResponseWriter to capture the status code while
// preserving Flush/Hijack/ReadFrom passthroughs for streaming compatibility.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker by delegating to the embedded ResponseWriter
// when it supports hijacking, preserving support for protocols like websockets.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// ReadFrom implements io.ReaderFrom by delegating to the embedded ResponseWriter
// when supported, preserving sendfile-style optimizations.
func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// p99Budget is the latency budget used to decide when to emit a slow-request span event.
const p99Budget = 750 * time.Millisecond

// RequestOutcomeMiddleware records the request outcome counter (success vs error
// class), adds error.type span attributes on 5xx, and emits a slow-request span
// event when the P99 latency budget is exceeded. It must run after otelhttp's
// middleware (so a span exists) and after Recoverer (so panics are recovered
// before we observe the outcome), and before auth/validation middleware so
// their rejections are still counted for availability purposes at this layer.
func RequestOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		activeRequests.Add(1)
		defer func() { activeRequests.Add(-1) }()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		route := RouteTemplate(r)

		outcome := "success"
		if rec.status >= 500 {
			outcome = "error"
		} else if rec.status >= 400 {
			outcome = "client_error"
		}

		requestOutcomeCounter.Add(r.Context(), 1,
			metric.WithAttributes(
				attribute.String("http.route", route),
				attribute.String("outcome", outcome),
				attribute.Int("http.response.status_code", rec.status),
			),
		)

		span := otrace.SpanFromContext(r.Context())
		if rec.status >= 500 {
			span.SetAttributes(ErrorTypeAttr("server_error"))
		}
		if duration > p99Budget {
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.String("http.route", route),
				attribute.Float64("duration_ms", float64(duration.Milliseconds())),
			))
		}
	})
}

// AuthOutcomeMiddleware wraps an existing auth/JWT middleware, recording an
// auth attempt outcome counter based on whether the wrapped middleware allowed
// the request through (response status < 401/403 range reaching downstream)
// or short-circuited it as denied.
func AuthOutcomeMiddleware(authMiddleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := authMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			wrapped.ServeHTTP(rec, r)

			outcome := "allowed"
			reason := "none"
			if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
				outcome = "denied"
				reason = "jwt_rejected"
			}

			authAttemptCounter.Add(r.Context(), 1,
				metric.WithAttributes(
					attribute.String("outcome", outcome),
					attribute.String("reason", reason),
				),
			)

			validationOutcomeCounter.Add(r.Context(), 1,
				metric.WithAttributes(
					attribute.String("outcome", map[bool]string{true: "failed", false: "passed"}[outcome == "denied"]),
					attribute.String("step", "jwt_auth"),
				),
			)
		})
	}
}

// RecordFlowOutcome emits the terminal outcome counter and duration histogram
// for the primary end-to-end business flow. Call this once at the point where
// the flow reaches a terminal state (success or failure).
func RecordFlowOutcome(ctx context.Context, outcome string, duration time.Duration) {
	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	flowDurationHistogram.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("outcome", outcome)))
}

// RecordFlowEntry increments the flow-entry counter, independent of eventual outcome.
func RecordFlowEntry(ctx context.Context) {
	flowEntryCounter.Add(ctx, 1)
}

// StartFlowSpan starts a root span for the primary business flow, to be used
// as the entry point for E2E flow tracing.
func StartFlowSpan(ctx context.Context, flowName string) (context.Context, otrace.Span) {
	return tracer.Start(ctx, flowName)
}
