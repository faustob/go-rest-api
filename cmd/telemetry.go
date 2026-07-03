// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation — shared meter, tracer, and instruments
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	instrumentationScope = "github.com/benc-uk/go-rest-api"
	serviceName          = "go-rest-api"
	// P99 budget in milliseconds — requests exceeding this get a span event
	p99BudgetMs = 750
)

// activeRequestCount tracks in-flight HTTP requests for the saturation gauge.
var activeRequestCount int64

// Instruments — defined once, recorded at call sites.
var (
	// http.server.request.duration histogram (latency + availability + error-rate + throughput)
	httpRequestDuration metric.Float64Histogram

	// http.server.active_requests up-down counter (saturation — in-flight)
	httpActiveRequests metric.Int64UpDownCounter

	// auth.attempts counter (auth failure rate)
	authAttempts metric.Int64Counter

	// flow.outcomes counter (e2e flow success rate + throughput)
	flowOutcomes metric.Int64Counter

	// flow.duration histogram (e2e flow latency P95 + freshness)
	flowDuration metric.Float64Histogram

	// flow.validation.outcomes counter (validation failure rate)
	flowValidationOutcomes metric.Int64Counter
)

// initTelemetry builds and globally registers the TracerProvider and
// MeterProvider. It returns a shutdown function that must be deferred by main.
func initTelemetry(ctx context.Context) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	// ── Trace exporter ────────────────────────────────────────────────────────
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	// ── Metric exporter ───────────────────────────────────────────────────────
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// ── Create instruments ────────────────────────────────────────────────────
	meter := otel.Meter(instrumentationScope)

	httpRequestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of HTTP server requests"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	httpActiveRequests, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP server requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total authentication attempts, tagged by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, err
	}

	flowOutcomes, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return nil, err
	}

	flowDuration, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary request flow"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	flowValidationOutcomes, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcomes of per-step request validation"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return nil, err
	}

	shutdown := func(ctx context.Context) error {
		if err := tp.Shutdown(ctx); err != nil {
			log.Printf("trace provider shutdown error: %v", err)
		}
		if err := mp.Shutdown(ctx); err != nil {
			log.Printf("metric provider shutdown error: %v", err)
		}
		return nil
	}
	return shutdown, nil
}

// recordHTTPRequest records all HTTP-server metrics for a single completed
// request. It is called by the otelMiddleware wrapper in server.go.
func recordHTTPRequest(
	ctx context.Context,
	method string,
	route string,
	statusCode int,
	durationMs float64,
) {
	outcome := "success"
	if statusCode >= 500 {
		outcome = "error"
	} else if statusCode >= 400 {
		outcome = "client_error"
	}

	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", method),
		attribute.String("http.route", route),
		attribute.Int("http.response.status_code", statusCode),
		attribute.String("outcome", outcome),
	}

	httpRequestDuration.Record(ctx, durationMs, metric.WithAttributes(attrs...))

	// Flow-level metrics: every HTTP request is one flow invocation.
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("outcome", outcome),
	))
	flowDuration.Record(ctx, durationMs, metric.WithAttributes(
		attribute.String("http.route", route),
	))
}

// recordAuthAttempt records one authentication decision.
func recordAuthAttempt(ctx context.Context, outcome string, reason string) {
	authAttempts.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("reason", reason),
	))
}

// recordValidationOutcome records one validation step result.
func recordValidationOutcome(ctx context.Context, step string, outcome string) {
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("step", step),
		attribute.String("outcome", outcome),
	))
}

// otelMiddleware wraps an http.Handler, tracking active requests, recording
// the request duration histogram, and adding a span event for slow requests.
func otelMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()

		// Track in-flight requests (saturation).
		atomic.AddInt64(&activeRequestCount, 1)
		httpActiveRequests.Add(ctx, 1)
		defer func() {
			atomic.AddInt64(&activeRequestCount, -1)
			httpActiveRequests.Add(ctx, -1)
		}()

		// Wrap the ResponseWriter to capture the status code.
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		durationMs := float64(time.Since(start).Milliseconds())

		// Derive the matched route template from chi's route context.
		routePattern := r.URL.Path // fallback
		if rctx := chi.RouteContext(ctx); rctx != nil && rctx.RoutePattern() != "" {
			routePattern = rctx.RoutePattern()
		}

		recordHTTPRequest(ctx, r.Method, routePattern, rw.status, durationMs)

		// Slow-request span event for P99 triage.
		if durationMs > p99BudgetMs {
			span := trace.SpanFromContext(ctx)
			span.AddEvent("slow_request",
				trace.WithAttributes(
					attribute.String("http.route", routePattern),
					attribute.Float64("duration.ms", durationMs),
					attribute.Int("http.response.status_code", rw.status),
				),
			)
		}
	})
}

// statusRecorder captures the HTTP status code written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
