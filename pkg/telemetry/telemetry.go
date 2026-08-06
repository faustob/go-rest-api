// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap & shared instrumentation for the API.
//
// This package owns the SINGLE meter/tracer for the service and every
// instrument created from it. The SDK is built and registered globally by
// InitOTel, which is called once from main() in cmd/server.go.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"log"
	"net/http"
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

// scopeName is the instrumentation scope for this service.
const scopeName = "github.com/benc-uk/go-rest-api"

// p99Budget is the P99 latency objective; handlers slower than this get a span
// event so the tail can be triaged from the trace.
const p99Budget = 750 * time.Millisecond

// ONE meter and ONE tracer for the whole service. Every instrument below is
// created from these. Go's OTel API rebinds instruments created before the SDK
// registers, so package-level creation is safe here.
var (
	meter  = otel.Meter(scopeName)
	tracer = otel.Tracer(scopeName)
)

// Instruments. Each one is recorded to below.
var (
	// requestsTotal powers availability, 5xx error rate and throughput SLIs:
	// it is dimensioned by route template, method, status code and outcome class.
	requestsTotal metric.Int64Counter

	// authAttempts powers the authentication failure rate SLI.
	authAttempts metric.Int64Counter
)

func init() {
	var err error

	requestsTotal, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of inbound HTTP requests by route, status and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create request outcome counter: %s", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome and denial reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### 📊 Telemetry: failed to create auth attempt counter: %s", err)
	}
}

// InitOTel builds the OpenTelemetry SDK and registers it as the global
// provider. It returns a shutdown function that flushes buffered telemetry.
//
// The OTLP endpoint is taken from the environment (OTEL_EXPORTER_OTLP_ENDPOINT)
// by the exporters themselves — nothing is hardcoded here.
//
// Registration is defensive: if anything fails we log and carry on with
// whatever provider is already registered, so the app always starts.
func InitOTel(ctx context.Context, serviceName string, version string) func(context.Context) error {
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
		log.Printf("### 📊 Telemetry: resource error, using default: %s", err)

		res = resource.Default()
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		log.Printf("### 📊 Telemetry: trace exporter disabled: %s", err)
		return noop
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		log.Printf("### 📊 Telemetry: metric exporter disabled: %s", err)
		return noop
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("### 📊 Telemetry: OpenTelemetry enabled for %s", serviceName)

	return func(shutdownCtx context.Context) error {
		tErr := tp.Shutdown(shutdownCtx)
		mErr := mp.Shutdown(shutdownCtx)

		if tErr != nil {
			return tErr
		}

		return mErr
	}
}

// ChiRouteTelemetry adds the matched chi route TEMPLATE to the telemetry that
// otelhttp emits, records the request outcome counter, and marks requests that
// exceed the P99 latency budget with a span event.
//
// The route pattern is read AFTER next.ServeHTTP returns, because chi has not
// routed the request when the middleware is entered. The response status is
// obtained from otelhttp's own bookkeeping rather than by wrapping the
// ResponseWriter, so streaming/SSE/hijack capabilities are fully preserved.
func ChiRouteTelemetry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		next.ServeHTTP(w, r)

		elapsed := time.Since(start)

		// Route TEMPLATE, e.g. /things/{id} — never the raw path.
		route := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}

		if route == "" {
			route = "unmatched"
		}

		// Tag otelhttp's http.server.request.duration histogram with the route
		// template so P95/P99 can be sliced per route.
		if labeler, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			labeler.Add(semconv.HTTPRoute(route))
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.HTTPRoute(route),
		}

		if requestsTotal != nil {
			requestsTotal.Add(r.Context(), 1, metric.WithAttributes(attrs...))
		}

		// Slow-request span event for P99 triage, on the span otelhttp created.
		if elapsed > p99Budget {
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				span.AddEvent("slow_request", trace.WithAttributes(
					semconv.HTTPRoute(route),
					attribute.Float64("duration_seconds", elapsed.Seconds()),
					attribute.Float64("budget_seconds", p99Budget.Seconds()),
				))
			}
		}
	})
}

// RecordAuthAttempt records one authentication/authorization decision.
// reason is a low-cardinality denial CLASS (never a message, token or user id)
// and is only attached when the attempt was denied.
func RecordAuthAttempt(ctx context.Context, allowed bool, reason string) {
	if authAttempts == nil {
		return
	}

	outcome := "allowed"

	attrs := []attribute.KeyValue{}

	if !allowed {
		outcome = "denied"

		if reason != "" {
			attrs = append(attrs, attribute.String("error.type", reason))
		}
	}

	attrs = append(attrs, attribute.String("outcome", outcome))

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// Tracer returns the service's single tracer for ad-hoc spans.
func Tracer() trace.Tracer {
	return tracer
}
