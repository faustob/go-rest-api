// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap and instrumentation for go-rest-api
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"bufio"
	"io"
	"net"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// meter is the single package-level meter for this service. Every instrument
// and every RegisterCallback MUST originate from this meter.
var meter = otel.Meter("go-rest-api")
var tracer = otel.Tracer("go-rest-api")

var (
	requestDuration      metric.Float64Histogram
	requestOutcomeTotal  metric.Int64Counter
	authAttemptsTotal    metric.Int64Counter
	tenantRequestTotal   metric.Int64Counter
	flowOutcomesTotal    metric.Int64Counter
	flowEntryTotal       metric.Int64Counter
	flowValidationTotal  metric.Int64Counter
	flowDuration         metric.Float64Histogram
	flowFreshness        metric.Float64Histogram
	poolSizeConfigured   int64 = 100 // default worker pool size, adjust to real config if known
	activeRequestCount   int64
)

// init eagerly creates all instruments from the package-level meter so that
// they are never nil, even if SetupOTelSDK is never called or fails before
// reaching initInstruments. otel.Meter(...) returns a valid meter backed by
// the no-op provider until a real provider is registered, so instrument
// creation here cannot fail due to provider absence.
func init() {
	if err := initInstruments(); err != nil {
		fmt.Println("otel: failed to initialize instruments:", err)
	}
}

// SetupOTelSDK builds the TracerProvider and MeterProvider, registers them
// globally, and returns a shutdown function. Registration is defensive: if a
// global provider is already set (e.g. by an agent) we tolerate it and
// continue using whatever is registered.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error

	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	res, err := sdkresource.Merge(
		sdkresource.Default(),
		sdkresource.NewSchemaless(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return shutdown, err
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)

	// Defensive registration: guard against an already-registered global
	// provider (e.g. attached by an external mechanism) causing a panic.
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("otel: tracer provider already registered, continuing with existing global:", r)
			}
		}()
		otel.SetTracerProvider(tracerProvider)
	}()

	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("otel: meter provider already registered, continuing with existing global:", r)
			}
		}()
		otel.SetMeterProvider(meterProvider)
	}()

	return shutdown, nil
}

func initInstruments() error {
	var err error

	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests"),
	)
	if err != nil {
		return err
	}

	requestOutcomeTotal, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithUnit("{request}"),
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
	)
	if err != nil {
		return err
	}

	authAttemptsTotal, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithUnit("{attempt}"),
		metric.WithDescription("Count of authentication/authorization decisions"),
	)
	if err != nil {
		return err
	}

	tenantRequestTotal, err = meter.Int64Counter(
		"http.server.request.by_tenant",
		metric.WithUnit("{request}"),
		metric.WithDescription("Count of HTTP requests per tenant/API key"),
	)
	if err != nil {
		return err
	}

	flowOutcomesTotal, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithUnit("{flow}"),
		metric.WithDescription("Count of terminal outcomes for the primary business flow"),
	)
	if err != nil {
		return err
	}

	flowEntryTotal, err = meter.Int64Counter(
		"flow.entries",
		metric.WithUnit("{flow}"),
		metric.WithDescription("Count of entries into the primary business flow"),
	)
	if err != nil {
		return err
	}

	flowValidationTotal, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithUnit("{validation}"),
		metric.WithDescription("Count of per-step validation outcomes within the primary flow"),
	)
	if err != nil {
		return err
	}

	flowDuration, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithUnit("s"),
		metric.WithDescription("End-to-end duration of the primary business flow"),
	)
	if err != nil {
		return err
	}

	flowFreshness, err = meter.Float64Histogram(
		"flow.entry_to_terminal.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Wall-clock time between flow entry and terminal state"),
	)
	if err != nil {
		return err
	}

	activeRequests, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("Number of in-flight HTTP requests"),
	)
	if err != nil {
		return err
	}

	poolSize, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithUnit("{worker}"),
		metric.WithDescription("Configured size of the HTTP server worker pool"),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequests, atomic.LoadInt64(&activeRequestCount))
		o.ObserveInt64(poolSize, poolSizeConfigured)
		return nil
	}, activeRequests, poolSize)
	if err != nil {
		return err
	}

	return nil
}

// httpStatusClass returns a low-cardinality outcome class for a status code.
func httpStatusClass(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	case status >= 300:
		return "redirect"
	default:
		return "success"
	}
}

// statusRecorder wraps http.ResponseWriter to capture the status code while
// preserving the full contract of the wrapped writer (Flusher, Hijacker,
// ReaderFrom) so hijacking (websockets) and sendfile optimizations continue
// to work through the wrapper.
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

// Hijack delegates to the underlying ResponseWriter's Hijacker implementation,
// if present, so connection hijacking (e.g. websocket upgrades) keeps working
// through this wrapper.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// ReadFrom delegates to the underlying ResponseWriter's io.ReaderFrom
// implementation, if present, preserving sendfile-style optimizations. If the
// wrapped writer doesn't implement it, fall back to a generic copy that
// still tracks the status as written.
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

// contextKey is a private type for context keys defined in this package to
// avoid collisions with keys from other packages.
type contextKey int

const statusRecorderContextKey contextKey = iota

// RequestTelemetryMiddleware records http.server.request.duration, the
// request outcome counter, per-tenant throughput, active-request gauge
// input, flow entry/outcome counters, and slow-request span events for the
// P99 budget.
func RequestTelemetryMiddleware(next http.Handler) http.Handler {
	const p99BudgetSeconds = 0.750

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		atomic.AddInt64(&activeRequestCount, 1)
		defer atomic.AddInt64(&activeRequestCount, -1)

		ctx, span := tracer.Start(r.Context(), "http.server.request")
		r = r.WithContext(ctx)

		if flowEntryTotal != nil {
			flowEntryTotal.Add(ctx, 1)
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		r = r.WithContext(context.WithValue(r.Context(), statusRecorderContextKey, rec))

		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		durationSeconds := duration.Seconds()

		// Route template is only populated AFTER routing has happened.
		route := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}
		if route == "" {
			route = "unmatched"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", schemeOf(r)),
			attribute.Int("http.response.status_code", rec.status),
			attribute.String("http.route", route),
		}

		if rec.status >= 500 {
			attrs = append(attrs, attribute.String("error.type", "HTTPServerError"))
			span.SetAttributes(attribute.String("error.type", "HTTPServerError"))
		}

		if requestDuration != nil {
			requestDuration.Record(ctx, durationSeconds, metric.WithAttributes(attrs...))
		}

		outcomeClass := httpStatusClass(rec.status)
		if requestOutcomeTotal != nil {
			requestOutcomeTotal.Add(ctx, 1, metric.WithAttributes(
				attribute.String("http.route", route),
				attribute.String("outcome", outcomeClass),
			))
		}

		flowOutcome := "success"
		if rec.status >= 500 {
			flowOutcome = "failure"
		}
		if flowOutcomesTotal != nil {
			flowOutcomesTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", flowOutcome)))
		}
		if flowDuration != nil {
			flowDuration.Record(ctx, durationSeconds, metric.WithAttributes(attribute.String("http.route", route)))
		}
		if flowFreshness != nil {
			flowFreshness.Record(ctx, durationSeconds, metric.WithAttributes(attribute.String("http.route", route)))
		}

		tenant := r.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}
		if tenantRequestTotal != nil {
			tenantRequestTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("tenant", tenant)))
		}

		if durationSeconds > p99BudgetSeconds {
			span.AddEvent("slow_request_p99_breach", trace.WithAttributes(
				attribute.Float64("duration.seconds", durationSeconds),
				attribute.String("http.route", route),
			))
		}

		span.End()
	})
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// AuthOutcomeMiddleware records an auth attempt outcome counter. It relies on
// the upstream JWT validator middleware having already run (this middleware
// is registered AFTER it in the chain) so a denial short-circuits before
// reaching here only if the validator itself writes the response — in that
// case this middleware's ServeHTTP body still records the outcome based on
// the response status observed via a wrapping recorder.
func AuthOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reuse the statusRecorder already installed by RequestTelemetryMiddleware
		// (stashed in the request context) instead of wrapping the response a
		// second time, which would compound loss of Hijacker/ReaderFrom support.
		// Fall back to a local recorder only if none is present in context, e.g.
		// if this middleware is used standalone.
		rec, ok := r.Context().Value(statusRecorderContextKey).(*statusRecorder)
		if ok && rec != nil {
			next.ServeHTTP(rec, r)
		} else {
			localRec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(localRec, r)
			rec = localRec
		}

		outcome := "granted"
		reason := "none"
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			outcome = "denied"
			reason = "invalid_or_missing_token"
		}

		if authAttemptsTotal != nil {
			authAttemptsTotal.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("outcome", outcome),
				attribute.String("reason", reason),
			))
		}
	})
}

// RecordFlowValidationOutcome emits a per-step validation outcome for the
// primary business flow, attaching the step name as a low-cardinality
// attribute.
func RecordFlowValidationOutcome(ctx context.Context, step string, outcome string) {
	if flowValidationTotal == nil {
		return
	}
	flowValidationTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("flow.step", step),
		attribute.String("outcome", outcome),
	))
}

