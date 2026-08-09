// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation for the go-rest-api service.
//
// This package owns the SINGLE meter/tracer for the service and every
// instrument created from it. InitOTel is called once from main().
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/middleware"
	chi5 "github.com/go-chi/chi/v5"

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

// scopeName is the instrumentation scope for this service.
const scopeName = "github.com/benc-uk/go-rest-api"

// p99Budget is the P99 latency SLO budget; handlers slower than this get a span event.
const p99Budget = 750 * time.Millisecond

// ONE meter and ONE tracer for the whole service. Every instrument below is
// created from this meter. Go's OTel API rebinds these once InitOTel registers
// the SDK, so package-level creation is safe.
var (
	meter  = otel.Meter(scopeName)
	tracer = otel.Tracer(scopeName)
)

// The instruments. Each is recorded to in this file (Middleware /
// RecordAuthAttempt) - none are declared without a measurement site.
var (
	requestDuration metric.Float64Histogram
	requestsTotal   metric.Int64Counter
	authAttempts    metric.Int64Counter
)

func init() {
	var err error

	// Route-level latency histogram, in SECONDS per semconv. Backs the P95 & P99 SLIs.
	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create http.server.request.duration: %s", err)
	}

	// Request outcome counter. Backs the availability, 5xx error-rate and throughput SLIs.
	requestsTotal, err = meter.Int64Counter(
		"http.server.requests",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create http.server.requests: %s", err)
	}

	// Auth decision counter. Backs the authentication failure-rate SLI.
	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create auth.attempts: %s", err)
	}
}

// InitOTel builds the trace & metric providers and registers them globally.
// The OTLP endpoint is env-driven via OTEL_EXPORTER_OTLP_ENDPOINT.
// It always returns a non-nil shutdown func, safe to call even after an error.
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

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return noop, err
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return func(shutdownCtx context.Context) error { return traceExp.Shutdown(shutdownCtx) }, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	// Registering the global providers in Go is tolerant: if a runtime has
	// already set one, the OTel API logs and keeps the existing provider rather
	// than panicking, so this is safe with or without an external agent.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("### OTel: providers registered for service %s", serviceName)

	return func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}, nil
}

// Middleware records the request duration histogram and the outcome counter for
// every request, and adds a span event when a handler blows the P99 budget.
//
// It uses chi's own WrapResponseWriter to capture the status code, which
// correctly forwards http.Flusher, http.Hijacker and io.ReaderFrom, so SSE,
// streaming and connection upgrades keep working.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww, ok := w.(middleware.WrapResponseWriter)
		if !ok {
			ww = middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		}

		start := time.Now()

		next.ServeHTTP(ww, r)

		elapsed := time.Since(start)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		// Route TEMPLATE, only populated AFTER routing has happened. Unmatched
		// requests get an empty route rather than the raw (unbounded) path.
		route := ""
		if rctx := chi5.RouteContext(r.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLScheme(scheme),
			semconv.HTTPResponseStatusCode(status),
			semconv.HTTPRoute(route),
			semconv.NetworkProtocolVersion(r.Proto),
		}

		// error.type is the low-cardinality status CLASS, never a message.
		if status >= 500 {
			attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		}

		set := metric.WithAttributes(attrs...)

		requestDuration.Record(r.Context(), elapsed.Seconds(), set)
		requestsTotal.Add(r.Context(), 1, set)

		// Slow-request span event for P99 triage, and the status class on the span
		// so 5xx responses can be attributed to a root-cause class.
		if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
			span.SetAttributes(semconv.HTTPRoute(route), semconv.HTTPResponseStatusCode(status))

			if status >= 500 {
				span.SetAttributes(semconv.ErrorTypeKey.String(strconv.Itoa(status)))
			}

			if elapsed > p99Budget {
				span.AddEvent("slow_request", trace.WithAttributes(
					attribute.Float64("duration_s", elapsed.Seconds()),
					attribute.Float64("budget_s", p99Budget.Seconds()),
					semconv.HTTPRoute(route),
				))
			}
		}
	})
}

// RecordAuthAttempt records one authentication/authorization decision.
// reason is a low-cardinality denial class and is ignored when allowed is true.
func RecordAuthAttempt(ctx context.Context, allowed bool, reason string) {
	outcome := "denied"
	if allowed {
		outcome = "allowed"
	}

	attrs := []attribute.KeyValue{attribute.String("outcome", outcome)}
	if !allowed && reason != "" {
		attrs = append(attrs, attribute.String("reason", reason))
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// Tracer exposes the service's single tracer for handler-level spans.
func Tracer() trace.Tracer {
	return tracer
}
