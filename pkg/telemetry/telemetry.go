// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation for the go-rest-api service.
//
// This package owns the ONE meter and the ONE tracer for the service, plus the
// SDK bootstrap that main() invokes. All instruments are created here and are
// recorded from the middleware below and from pkg/auth.
//
// Export is over OTLP/gRPC and configured entirely from the environment
// (OTEL_EXPORTER_OTLP_ENDPOINT and friends); nothing is hardcoded.
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// scopeName is the instrumentation scope for both the meter and the tracer.
const scopeName = "github.com/benc-uk/go-rest-api"

// p99Budget is the P99 latency budget; handlers slower than this get a span
// event so the slow tail can be triaged from the trace.
const p99Budget = 750 * time.Millisecond

// ONE meter and ONE tracer for the whole service. These resolve against the
// global providers, which InitSDK registers at startup (Go rebinds instruments
// created before registration, so package level creation is safe).
var (
	meter  = otel.Meter(scopeName)
	tracer = otel.Tracer(scopeName)
)

// Instruments, each created exactly once and recorded from the sites below.
var (
	requestDuration metric.Float64Histogram
	requestCount    metric.Int64Counter
	authAttempts    metric.Int64Counter
)

func init() {
	var err error

	// OTel semantic convention: inbound request duration, in SECONDS.
	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP server requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create http.server.request.duration: %s", err)
	}

	// Request outcome counter, powering availability, error rate & throughput.
	requestCount, err = meter.Int64Counter(
		"http.server.request",
		metric.WithDescription("Count of inbound HTTP server requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create http.server.request: %s", err)
	}

	// Auth attempt outcome counter, powering the auth failure rate SLI.
	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome and denial reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create auth.attempts: %s", err)
	}
}

// InitSDK builds the OpenTelemetry SDK and registers it as the global tracer &
// meter provider. It is called ONCE from main(). Traces are batched to an OTLP
// gRPC exporter and metrics are pushed by a PeriodicReader, so everything
// recorded here has a real path off the process. The returned func shuts
// everything down and flushes buffered telemetry; it is never nil, so callers
// can always defer it even when initialisation failed.
func InitSDK(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return noop, err
	}

	// Endpoint & credentials come from OTEL_EXPORTER_OTLP_ENDPOINT etc.
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return noop, err
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return noop, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Register defensively: otel.Set* replaces rather than panics, and reports
	// via the otel error handler if a provider was already installed, so the
	// app starts correctly with or without an external bootstrap.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("### 📡 OTel: SDK registered for service '%s'", serviceName)

	return func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}, nil
}

// Middleware is chi/net-http middleware that emits the OTel semantic
// convention HTTP server metrics and a server span for every request.
//
// It must be registered AFTER middleware.Recoverer and BEFORE any auth
// middleware, so short circuited (401) responses are still observed.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		ctx, span := tracer.Start(r.Context(), r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLScheme(scheme(r)),
				semconv.NetworkProtocolVersion(protoVersion(r)),
			),
		)
		defer span.End()

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Propagate the span context down to the handlers so their spans nest.
		r = r.WithContext(ctx)
		next.ServeHTTP(recorder, r)

		elapsed := time.Since(start)

		// chi only populates the RouteContext once routing has happened, so the
		// route TEMPLATE must be read AFTER next.ServeHTTP returns. Never use
		// the raw path, it is unbounded cardinality.
		route := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}

		if route == "" {
			route = "unmatched"
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLScheme(scheme(r)),
			semconv.HTTPRoute(route),
			semconv.HTTPResponseStatusCode(recorder.status),
			semconv.NetworkProtocolVersion(protoVersion(r)),
		}

		// error.type is the low cardinality status CLASS, never a message.
		if recorder.status >= 400 {
			attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(recorder.status)))
		}

		// Duration in SECONDS per semantic conventions.
		requestDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))

		// Outcome counter for availability / error rate / throughput SLIs.
		requestCount.Add(ctx, 1, metric.WithAttributes(
			append(attrs, attribute.String("http.response.status_class", statusClass(recorder.status)))...,
		))

		span.SetAttributes(attrs...)
		span.SetName(r.Method + " " + route)

		// Map the response status to a root cause class on the server span so
		// 5xx responses can be attributed without scanning logs.
		if recorder.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(recorder.status))
		}

		// Slow request span event for P99 triage.
		if elapsed > p99Budget {
			span.AddEvent("http.slow_request", trace.WithAttributes(
				attribute.Float64("http.server.request.duration", elapsed.Seconds()),
				attribute.Float64("http.server.request.budget", p99Budget.Seconds()),
				semconv.HTTPRoute(route),
			))
		}
	})
}

// RecordAuthOutcome records one authentication/authorization decision.
// outcome is "allowed" or "denied"; reason is a low cardinality denial class
// and is omitted when empty. Never pass tokens, user ids or error messages.
func RecordAuthOutcome(ctx context.Context, outcome string, reason string) {
	attrs := []attribute.KeyValue{
		attribute.String("auth.outcome", outcome),
	}

	if reason != "" {
		attrs = append(attrs,
			attribute.String("auth.deny_reason", reason),
			semconv.ErrorTypeKey.String(reason),
		)
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	return "http"
}

func protoVersion(r *http.Request) string {
	switch r.ProtoMajor {
	case 1:
		if r.ProtoMinor == 0 {
			return "1.0"
		}

		return "1.1"
	case 2:
		return "2"
	case 3:
		return "3"
	}

	return ""
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	}

	return "1xx"
}

// statusRecorder captures the response status code. It forwards every optional
// interface the wrapped ResponseWriter may implement, so SSE, streaming,
// WebSocket upgrades and the io.Copy fast path keep working.
type statusRecorder struct {
	http.ResponseWriter

	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.written {
		s.status = status
		s.written = true
	}

	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.written = true

	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}

	return nil, nil, errors.New("telemetry: underlying ResponseWriter does not implement http.Hijacker")
}

func (s *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	s.written = true

	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}

	return io.Copy(s.ResponseWriter, src)
}

func (s *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := s.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}

	return http.ErrNotSupported
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}
