// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap & shared instruments for the API
//
// InitOTel is called ONCE from main() (cmd/server.go) and registers the global
// tracer & meter providers - this is the app's sole SDK bootstrap (Go has no
// agent). Every instrument here is created from the single package level meter.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const scopeName = "github.com/benc-uk/go-rest-api"

// P99 latency budget for the service, a span event is added when it is exceeded
const slowRequestBudget = 750 * time.Millisecond

// ONE meter & tracer for the whole service
var (
	meter  = otel.Meter(scopeName)
	tracer = otel.Tracer(scopeName)
)

// Shared instruments, created exactly once from the meter above.
// Names use one style: dotted semconv naming rooted at http.server.*
var (
	requestDuration metric.Float64Histogram
	requestsTotal   metric.Int64Counter
	authAttempts    metric.Int64Counter
)

func init() {
	var err error

	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create http.server.request.duration: %s", err)
	}

	requestsTotal, err = meter.Int64Counter(
		"http.server.requests",
		metric.WithDescription("Count of inbound HTTP requests by route, outcome class and tenant tier"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create http.server.requests: %s", err)
	}

	authAttempts, err = meter.Int64Counter(
		"http.server.auth.attempts",
		metric.WithDescription("Authentication & authorization decisions by outcome and denial reason"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create http.server.auth.attempts: %s", err)
	}
}

// InitOTel builds and registers the global OpenTelemetry providers.
// The OTLP endpoint comes from the standard OTEL_EXPORTER_OTLP_ENDPOINT env var.
// It returns a shutdown function that flushes buffered telemetry.
func InitOTel(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	))
	if err != nil {
		return noop, err
	}

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

	// Register BOTH providers unconditionally: this is the app's only SDK bootstrap, and
	// skipping either would leave those instruments bound to the no-op provider so nothing
	// is exported.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	shutdown := func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}

	return shutdown, nil
}

// RecordAuthAttempt records one authentication / authorization decision.
// outcome is "allowed" or "denied"; reason is the denial class (empty when allowed).
func RecordAuthAttempt(ctx context.Context, outcome, reason, method string) {
	if authAttempts == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
		attribute.String("auth.method", method),
	}

	if reason != "" {
		attrs = append(attrs, attribute.String("error.type", reason))
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// HTTPMiddleware is chi middleware creating the inbound server span and emitting the
// semantic convention http.server.request.duration histogram (SECONDS) plus a per
// route/outcome/tenant request counter. The chi route pattern is only available AFTER
// the next handler has run, so all recording happens there.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Continue any inbound trace context from the caller
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		ctx, span := tracer.Start(ctx, "HTTP "+r.Method, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		start := time.Now()

		// chi's wrapper forwards Flusher / Hijacker / Pusher / ReaderFrom for us
		ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r.WithContext(ctx))

		elapsed := time.Since(start)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		// Route TEMPLATE (never the raw path) - populated once routing has happened
		route := "unmatched"
		if rc := chi.RouteContext(ctx); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		outcome := "success"

		switch {
		case status >= 500:
			outcome = "server_error"
		case status >= 400:
			outcome = "client_error"
		}

		// Business/cohort dimension for cohort aware latency & throughput SLOs
		tier := r.Header.Get("X-Tenant-Tier")
		if tier == "" {
			tier = "unknown"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", scheme),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
			attribute.String("network.protocol.version", strconv.Itoa(r.ProtoMajor)+"."+strconv.Itoa(r.ProtoMinor)),
			attribute.String("tenant.tier", tier),
		}

		if status >= 500 {
			attrs = append(attrs, attribute.String("error.type", strconv.Itoa(status)))
		}

		if requestDuration != nil {
			requestDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))
		}

		if requestsTotal != nil {
			requestsTotal.Add(ctx, 1, metric.WithAttributes(
				append(attrs, attribute.String("http.outcome", outcome))...))
		}

		// Name the span with the low cardinality route template & record the outcome class
		span.SetName(r.Method + " " + route)
		span.SetAttributes(attrs...)

		if status >= 500 {
			span.SetStatus(codes.Error, "server error")
		}

		// Slow request span event for P99 triage
		if elapsed > slowRequestBudget {
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.Float64("duration", elapsed.Seconds()),
				attribute.Float64("budget", slowRequestBudget.Seconds()),
				attribute.String("http.route", route),
			))
		}
	})
}

// Tracer exposes the single service tracer for handlers that want child spans
func Tracer() trace.Tracer {
	return tracer
}
