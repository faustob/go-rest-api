// ----------------------------------------------------------------------------
// OpenTelemetry setup & instrumentation for the API.
//
// This file owns the SINGLE meter for the service and every instrument created
// from it. InitOTel builds and registers the global SDK and is called ONCE from
// main(); everything else in pkg/ simply uses the registered global provider.
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
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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

// scopeName is the single instrumentation scope for this service.
const scopeName = "github.com/benc-uk/go-rest-api"

// p99BudgetSeconds is the P99 latency objective (750ms). Requests slower than
// this get a span event so the tail can be triaged from the trace.
const p99BudgetSeconds = 0.750

// ONE meter and ONE tracer per service. Every instrument below is created from
// this meter. These bind lazily to whatever provider is globally registered,
// and Go's OTel API rebinds them after InitOTel runs.
var (
	meter  = otel.Meter(scopeName)
	tracer = otel.Tracer(scopeName)
)

// Instruments. Created exactly once, here, and recorded from the middleware /
// helpers in this same package.
var (
	requestDuration metric.Float64Histogram
	requestCount    metric.Int64Counter
	authAttempts    metric.Int64Counter
)

func init() {
	var err error

	// Semantic-convention inbound request duration: histogram, in SECONDS.
	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create http.server.request.duration: %s", err)
	}

	// Request outcome counter: powers availability, 5xx error rate and throughput.
	requestCount, err = meter.Int64Counter(
		"http.server.request.total",
		metric.WithDescription("Count of inbound HTTP requests by route, status and outcome class"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create http.server.request.total: %s", err)
	}

	// Auth decision outcome counter, tagged with the denial reason.
	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome and reason"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create auth.attempts: %s", err)
	}
}

// InitOTel builds the OpenTelemetry SDK and registers it as the GLOBAL provider.
// Call this ONCE, from main(), before serving traffic. The returned function
// shuts the providers down and flushes buffered telemetry.
//
// The OTLP endpoint is taken from the environment (OTEL_EXPORTER_OTLP_ENDPOINT)
// and is never hardcoded here.
func InitOTel(ctx context.Context, serviceName, version string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return noop, err
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return noop, err
	}

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return noop, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	// Register defensively: if something else (a sidecar/wrapper) already set a
	// provider, otel.Set* logs and keeps the existing one rather than crashing.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("### 📡 OTel: telemetry registered for service %s", serviceName)

	return func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}, nil
}

// statusRecorder captures the response status code for the metrics middleware.
// It forwards the OPTIONAL http interfaces so streaming, SSE, hijacking and the
// io.Copy fast path keep working exactly as before.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}

	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}

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

	return nil, nil, errors.New("http.Hijacker not supported by underlying ResponseWriter")
}

func (s *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := s.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}

	return http.ErrNotSupported
}

func (s *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		if !s.written {
			s.status = http.StatusOK
			s.written = true
		}

		return rf.ReadFrom(r)
	}

	return io.Copy(s.ResponseWriter, r)
}

// HTTPMiddleware returns chi-compatible middleware that emits server spans (via
// the otelhttp contrib integration) plus the semantic-convention HTTP server
// metrics. Install it with router.Use(...) so the router keeps its chi.Router
// type.
//
// The chi route TEMPLATE is only known AFTER routing has happened, so the route
// attribute is read once next.ServeHTTP returns - never the raw URL path.
func HTTPMiddleware(serviceName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			elapsed := time.Since(start).Seconds()

			// Routing has run by now, so the matched template is populated.
			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}

			if route == "" {
				route = "unmatched"
			}

			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}

			attrs := []attribute.KeyValue{
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLScheme(scheme),
				semconv.HTTPRoute(route),
				semconv.HTTPResponseStatusCode(rec.status),
				semconv.NetworkProtocolVersion(r.Proto),
			}

			// error.type is the low-cardinality status CLASS, never a message.
			outcome := outcomeClass(rec.status)
			if rec.status >= 500 {
				attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(rec.status)))
			}

			requestDuration.Record(r.Context(), elapsed, metric.WithAttributes(attrs...))
			requestCount.Add(r.Context(), 1, metric.WithAttributes(
				append(attrs, attribute.String("http.outcome", outcome))...,
			))

			// Tail-latency triage: mark the server span when the P99 budget is blown.
			if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
				span.SetAttributes(semconv.HTTPRoute(route))

				if rec.status >= 500 {
					// Attribute 5xx responses to a root-cause class on the span.
					span.SetAttributes(semconv.ErrorTypeKey.String(strconv.Itoa(rec.status)))
				}

				if elapsed > p99BudgetSeconds {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.Float64("duration_s", elapsed),
						attribute.Float64("budget_s", p99BudgetSeconds),
						semconv.HTTPRoute(route),
					))
				}
			}
		})

		// otelhttp provides the server span and context propagation.
		return otelhttp.NewHandler(metricsHandler, serviceName)
	}
}

// RecordAuthAttempt records one authentication/authorization decision.
// reason is a small fixed set of denial codes and is empty when allowed.
func RecordAuthAttempt(ctx context.Context, allowed bool, reason string) {
	outcome := "allowed"
	if !allowed {
		outcome = "denied"
	}

	attrs := []attribute.KeyValue{attribute.String("outcome", outcome)}
	if !allowed && reason != "" {
		attrs = append(attrs, attribute.String("error.type", reason))
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// Tracer exposes the service's single tracer for other packages.
func Tracer() trace.Tracer {
	return tracer
}

// outcomeClass maps a status code to a low-cardinality outcome class.
func outcomeClass(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}
