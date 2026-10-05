// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap and HTTP telemetry middleware for the REST API.
// Registers the global TracerProvider/MeterProvider, wired to an OTLP/gRPC
// endpoint (OTEL_EXPORTER_OTLP_ENDPOINT), and provides middleware that emits
// the standard http.server.request.duration histogram plus request-outcome,
// per-tenant, and auth-attempt counters.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// p99BudgetSeconds is the P99 latency budget (750ms) from the
// go-rest-api-http-latency-p99 SLI; requests slower than this get a span
// event for triage.
const p99BudgetSeconds = 0.75

var (
	meter = otel.Meter("go-rest-api")

	httpServerRequestDuration  metric.Float64Histogram
	httpServerRequestOutcome   metric.Int64Counter
	httpServerRequestsByTenant metric.Int64Counter
	authAttempts               metric.Int64Counter
)

// initInstruments creates every metric instrument from the package meter.
func initInstruments() error {
	var err error

	httpServerRequestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	httpServerRequestOutcome, err = meter.Int64Counter(
		"http.server.request.outcome",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	httpServerRequestsByTenant, err = meter.Int64Counter(
		"http.server.requests.by_tenant",
		metric.WithDescription("Count of inbound HTTP requests broken out by tenant tier"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	return nil
}

// setupOTel builds the TracerProvider and MeterProvider, registers them as
// the OpenTelemetry globals, and creates the metric instruments used by the
// middleware below. It returns a shutdown function that must be deferred by
// the caller so buffered spans/metrics are flushed on exit.
func setupOTel(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
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

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	meter = otel.Meter(serviceName)

	if err := initInstruments(); err != nil {
		return nil, err
	}

	shutdown := func(ctx context.Context) error {
		if err := tp.Shutdown(ctx); err != nil {
			return err
		}
		return mp.Shutdown(ctx)
	}

	return shutdown, nil
}

// statusRecorder wraps http.ResponseWriter to capture the response status
// code, while forwarding every optional interface the original writer may
// implement (Flush, Hijack, ReadFrom) so streaming/SSE/WebSocket/sendfile
// behaviour is preserved unchanged.
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

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support Hijack")
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// telemetryMiddleware records the standard http.server.request.duration
// histogram plus request-outcome and per-tenant counters, and annotates the
// active span (created by otelchi) with an error.type attribute and a
// slow-request event when the P99 latency budget is exceeded.
func telemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()

		route := r.URL.Path
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			if pattern := rctx.RoutePattern(); pattern != "" {
				route = pattern
			}
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		tenantTier := r.Header.Get("X-Tenant-Tier")
		if tenantTier == "" {
			tenantTier = "unknown"
		}

		httpServerRequestDuration.Record(r.Context(), duration, metric.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", scheme),
			attribute.Int("http.response.status_code", rec.status),
			attribute.String("http.route", route),
			attribute.String("app.tenant.tier", tenantTier),
		))

		outcome := "success"
		if rec.status >= 500 {
			outcome = "error"
		}

		httpServerRequestOutcome.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
			attribute.Int("http.response.status_code", rec.status),
		))

		httpServerRequestsByTenant.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("app.tenant.tier", tenantTier),
		))

		span := trace.SpanFromContext(r.Context())
		if rec.status >= 500 {
			span.SetAttributes(attribute.String("error.type", fmt.Sprintf("http_%d", rec.status)))
			span.SetStatus(codes.Error, fmt.Sprintf("http %d", rec.status))
		}

		if duration > p99BudgetSeconds {
			span.AddEvent("slow_request_p99_budget_exceeded", trace.WithAttributes(
				attribute.Float64("duration.seconds", duration),
				attribute.String("http.route", route),
			))
		}
	})
}

// authOutcomeMiddleware records an auth-attempt outcome counter for every
// request to a protected route. It wraps (and therefore runs before) the JWT
// validator middleware so it still observes denied (401/403) requests that
// the validator short-circuits.
func authOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		outcome := "allowed"
		reason := "ok"
		switch rec.status {
		case http.StatusUnauthorized:
			outcome = "denied"
			reason = "unauthorized"
		case http.StatusForbidden:
			outcome = "denied"
			reason = "forbidden"
		}

		authAttempts.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}
