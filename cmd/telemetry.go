// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap (traces + metrics) and HTTP telemetry middleware
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Single meter/tracer for the whole service - every instrument and callback below is created
// from this same meter, per the one-meter-per-service rule.
var (
	meter  = otel.Meter("go-rest-api")
	tracer = otel.Tracer("go-rest-api")
)

var (
	httpServerRequestDuration metric.Float64Histogram
	httpServerRequestOutcome  metric.Int64Counter
	httpServerActiveRequests  metric.Int64UpDownCounter
	httpServerWorkerPoolSize  metric.Int64ObservableGauge
	authAttempts              metric.Int64Counter
	tenantRequestCount        metric.Int64Counter
	flowEntries               metric.Int64Counter
	flowOutcomes              metric.Int64Counter
	flowDuration              metric.Float64Histogram
)

// maxWorkerPoolSize is the configured concurrency capacity reported by the worker-pool-size
// gauge, used alongside httpServerActiveRequests to compute saturation.
var maxWorkerPoolSize int64 = 100

func init() {
	if v := os.Getenv("MAX_WORKERS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			maxWorkerPoolSize = n
		}
	}
}

// initMetricInstruments creates all OpenTelemetry instruments used by the service. It must be
// called once during startup, after the MeterProvider has been configured (see initTelemetry).
func initMetricInstruments() error {
	var err error

	httpServerRequestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.request.duration histogram: %w", err)
	}

	httpServerRequestOutcome, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.request.outcomes counter: %w", err)
	}

	httpServerActiveRequests, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.active_requests counter: %w", err)
	}

	httpServerWorkerPoolSize, err = meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured maximum concurrent request handling capacity"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.worker_pool.size gauge: %w", err)
	}

	if _, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(httpServerWorkerPoolSize, maxWorkerPoolSize)
		return nil
	}, httpServerWorkerPoolSize); err != nil {
		return fmt.Errorf("failed to register worker pool size callback: %w", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
	)
	if err != nil {
		return fmt.Errorf("failed to create auth.attempts counter: %w", err)
	}

	tenantRequestCount, err = meter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("Count of HTTP requests by tenant"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.request.count counter: %w", err)
	}

	flowEntries, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of primary business flow entries"),
	)
	if err != nil {
		return fmt.Errorf("failed to create flow.entries counter: %w", err)
	}

	flowOutcomes, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of primary business flow terminal outcomes"),
	)
	if err != nil {
		return fmt.Errorf("failed to create flow.outcomes counter: %w", err)
	}

	flowDuration, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithUnit("s"),
		metric.WithDescription("End-to-end duration of the primary business flow"),
	)
	if err != nil {
		return fmt.Errorf("failed to create flow.duration histogram: %w", err)
	}

	return nil
}

// initTelemetry configures the OpenTelemetry SDK (traces + metrics), exporting via OTLP/gRPC,
// and registers it as the global provider. The OTLP endpoint is env-driven
// (OTEL_EXPORTER_OTLP_ENDPOINT). It returns a shutdown function that must be deferred by the
// caller so buffered spans/metrics flush on exit. Go has no attach-time agent, so the app always
// owns and registers the SDK here.
func initTelemetry(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:4317"
	}

	res, err := sdkresource.New(ctx,
		sdkresource.WithAttributes(
			attribute.String("service.name", serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create otel resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp metric exporter: %w", err)
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	if err := initMetricInstruments(); err != nil {
		return nil, err
	}

	return func(shutdownCtx context.Context) error {
		if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shutdown tracer provider: %w", err)
		}
		if err := meterProvider.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shutdown meter provider: %w", err)
		}
		return nil
	}, nil
}

// metricsMiddleware records OpenTelemetry metrics for every inbound HTTP request. Register it
// AFTER otelchi.Middleware (so a span already exists in the request context) and after
// middleware.Recoverer (so panics are still recovered upstream).
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		httpServerActiveRequests.Add(r.Context(), 1)

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		duration := time.Since(start)
		httpServerActiveRequests.Add(r.Context(), -1)

		// RoutePattern is only populated once routing has completed, i.e. after next.ServeHTTP.
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", scheme),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
		}

		if proto := strings.TrimPrefix(r.Proto, "HTTP/"); proto != "" {
			attrs = append(attrs, attribute.String("network.protocol.version", proto))
		}

		outcome := "success"
		span := trace.SpanFromContext(r.Context())
		if status >= 500 {
			outcome = "error"
			attrs = append(attrs, attribute.String("error.type", strconv.Itoa(status)))
			span.SetAttributes(attribute.String("error.type", strconv.Itoa(status)))
			span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", status))
		} else if status >= 400 {
			outcome = "client_error"
		}

		// Slow-request span event once the response exceeds the P99 latency budget (750ms).
		if duration > 750*time.Millisecond {
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.Float64("duration_seconds", duration.Seconds()),
				attribute.String("http.route", route),
			))
		}

		httpServerRequestDuration.Record(r.Context(), duration.Seconds(), metric.WithAttributes(attrs...))

		httpServerRequestOutcome.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
		))

		tenant := r.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}
		tenantRequestCount.Add(r.Context(), 1, metric.WithAttributes(attribute.String("tenant", tenant)))

		// Primary business flow: each HTTP request is treated as one flow entry/terminal event.
		flowEntries.Add(r.Context(), 1)
		flowOutcome := "success"
		if status >= 500 {
			flowOutcome = "failure"
		}
		flowOutcomes.Add(r.Context(), 1, metric.WithAttributes(attribute.String("outcome", flowOutcome)))
		flowDuration.Record(r.Context(), duration.Seconds())
	})
}

// authTelemetryMiddleware records the outcome of JWT authentication/authorization decisions,
// plus a per-request validation span. It must be registered BEFORE the JWT validator middleware
// on the protected router group so it also observes denied (401/403) requests.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "auth.validation", trace.WithSpanKind(trace.SpanKindInternal))
		r = r.WithContext(ctx)

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		outcome := "allowed"
		reason := "ok"
		passed := true
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			outcome = "denied"
			reason = strconv.Itoa(status)
			passed = false
		}
		span.SetAttributes(attribute.Bool("validation.passed", passed))

		authAttempts.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))

		span.End()
	})
}
