// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap and HTTP telemetry middleware for the REST API.
// Builds/registers the global Tracer & Meter providers and provides chi
// middleware that records the standard http.server.request.duration
// histogram plus custom outcome/tenant/auth counters.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/middleware"
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

const (
	otelScopeName = "github.com/benc-uk/go-rest-api"
	// slowRequestP99Budget is the latency budget used to flag slow requests
	// with a span event, per the HTTP Response Time P99 SLI (750ms).
	slowRequestP99Budget = 750 * time.Millisecond
)

var (
	otelTracer trace.Tracer
	otelMeter  metric.Meter

	httpServerRequestDuration metric.Float64Histogram
	httpRequestOutcomeCounter metric.Int64Counter
	httpTenantRequestCounter  metric.Int64Counter
	authAttemptsCounter       metric.Int64Counter
)

// setupOTel builds the OTLP-exporting Tracer & Meter providers, registers
// them as the OpenTelemetry globals, and creates the instruments used by the
// HTTP middleware below. It returns a shutdown func to be deferred by the
// caller so buffered telemetry is flushed on exit. The OTLP endpoint is
// configured via the standard OTEL_EXPORTER_OTLP_ENDPOINT environment
// variable.
func setupOTel(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build otel resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otel trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otel metric exporter: %w", err)
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	otelTracer = otel.Tracer(otelScopeName)
	otelMeter = otel.Meter(otelScopeName)

	httpServerRequestDuration, err = otelMeter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create http.server.request.duration histogram: %w", err)
	}

	httpRequestOutcomeCounter, err = otelMeter.Int64Counter(
		"http.server.request.outcome",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class (success/client_error/server_error)"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create http.server.request.outcome counter: %w", err)
	}

	httpTenantRequestCounter, err = otelMeter.Int64Counter(
		"http.server.request.tenant",
		metric.WithDescription("Count of inbound HTTP requests by tenant/API key"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create http.server.request.tenant counter: %w", err)
	}

	authAttemptsCounter, err = otelMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth.attempts counter: %w", err)
	}

	return func(shutdownCtx context.Context) error {
		if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return meterProvider.Shutdown(shutdownCtx)
	}, nil
}

// otelHTTPMiddleware records the standard http.server.request.duration
// histogram (OTel semantic conventions), a request outcome counter (for the
// availability SLI) and a per-tenant request counter (for the throughput
// SLI), plus a tracing span with a slow-request event (for the P99 SLI). It
// is registered on the top-level router, after middleware.Recoverer and
// before any auth/validation middleware, so every request/response is
// observed uniformly.
func otelHTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		ctx, span := otelTracer.Start(r.Context(), r.Method+" "+r.URL.Path)
		defer span.End()

		next.ServeHTTP(ww, r.WithContext(ctx))

		duration := time.Since(start)
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unknown"
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLScheme(scheme),
			semconv.HTTPRoute(route),
			semconv.HTTPResponseStatusCode(status),
		}

		outcome := "success"
		switch {
		case status >= 500:
			outcome = "server_error"
			span.SetAttributes(attribute.String("error.type", strconv.Itoa(status)))
			span.SetStatus(codes.Error, http.StatusText(status))
		case status >= 400:
			outcome = "client_error"
		}

		if duration > slowRequestP99Budget {
			span.AddEvent("slow.request.p99.budget.exceeded", trace.WithAttributes(
				attribute.Int64("duration.ms", duration.Milliseconds()),
				attribute.String("http.route", route),
			))
		}

		span.SetAttributes(attrs...)

		httpServerRequestDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))

		httpRequestOutcomeCounter.Add(ctx, 1, metric.WithAttributes(
			semconv.HTTPRoute(route),
			attribute.String("outcome", outcome),
		))

		tenant := r.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}
		httpTenantRequestCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tenant.id", tenant),
			semconv.HTTPRoute(route),
		))
	})
}

// authOutcomeMiddleware records an auth.attempts counter for every request
// to a protected route, tagged with the outcome (allowed/denied). It must be
// registered BEFORE the JWT validator middleware: a denied request
// short-circuits the chain there, so wrapping it from the outside is the
// only way to observe the rejection.
func authOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		outcome := "allowed"
		reason := "n/a"
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			outcome = "denied"
			reason = strconv.Itoa(status)
		}

		authAttemptsCounter.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}
