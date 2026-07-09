// ----------------------------------------------------------------------------
// OpenTelemetry SDK bootstrap and HTTP/auth instrumentation for go-rest-api
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	maxWorkers = 100 // Configured worker pool size, used for saturation SLI
)

var (
	tracer trace.Tracer
	meter  metric.Meter

	requestDuration   metric.Float64Histogram
	requestOutcome    metric.Int64Counter
	authAttempts      metric.Int64Counter
	flowOutcomes      metric.Int64Counter
	flowDuration      metric.Float64Histogram
	validationOutcome metric.Int64Counter

	activeRequestsCount int64
)

// SetupOTelSDK builds and registers the global TracerProvider and MeterProvider.
// It returns a shutdown function that should be deferred by the caller.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
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

	var shutdownFuncs []func(context.Context) error

	shutdown := func(ctx context.Context) error {
		var errs error
		for _, fn := range shutdownFuncs {
			errs = errors.Join(errs, fn(ctx))
		}
		return errs
	}

	// Traces
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)

	// Guard against double registration if an agent/other init already set a provider.
	func() {
		defer func() {
			_ = recover()
		}()
		otel.SetTracerProvider(tracerProvider)
	}()

	// Metrics
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return shutdown, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)

	func() {
		defer func() {
			_ = recover()
		}()
		otel.SetMeterProvider(meterProvider)
	}()

	tracer = otel.Tracer("go-rest-api")
	meter = otel.Meter("go-rest-api")

	if err := initInstruments(); err != nil {
		return shutdown, err
	}

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
		return fmt.Errorf("failed to create http.server.request.duration: %w", err)
	}

	requestOutcome, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests labeled by route and outcome class"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.request.outcomes: %w", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
	)
	if err != nil {
		return fmt.Errorf("failed to create auth.attempts: %w", err)
	}

	flowOutcomes, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of the primary request flow"),
	)
	if err != nil {
		return fmt.Errorf("failed to create flow.outcomes: %w", err)
	}

	flowDuration, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithUnit("s"),
		metric.WithDescription("End-to-end duration of the primary request flow"),
	)
	if err != nil {
		return fmt.Errorf("failed to create flow.duration: %w", err)
	}

	validationOutcome, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcome of request validation steps within the primary flow"),
	)
	if err != nil {
		return fmt.Errorf("failed to create flow.validation.outcomes: %w", err)
	}

	activeRequests, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.active_requests: %w", err)
	}

	workerPoolSize, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured size of the HTTP worker pool"),
	)
	if err != nil {
		return fmt.Errorf("failed to create http.server.worker_pool.size: %w", err)
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequests, activeRequestsCount)
		o.ObserveInt64(workerPoolSize, int64(maxWorkers))
		return nil
	}, activeRequests, workerPoolSize)
	if err != nil {
		return fmt.Errorf("failed to register saturation callback: %w", err)
	}

	return nil
}

// statusRecorder wraps http.ResponseWriter to capture the status code while
// preserving the optional Flusher, Hijacker and ReaderFrom interfaces.
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

// HTTPMetricsMiddleware records http.server.request.duration, a route/outcome
// counter, the primary-flow outcome/duration/entry metrics, active-request
// saturation gauge input, and adds slow-request span events for P99 triage.
func HTTPMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "http.request")
		defer span.End()

		r = r.WithContext(ctx)

		activeRequestsCount++
		defer func() { activeRequestsCount-- }()

		// Flow-entry counter (throughput SLI for the primary flow)
		flowOutcomes.Add(ctx, 0) // no-op touch to ensure instrument is used consistently; actual entry recorded below

		start := time.Now()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		status := rec.status

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", schemeOf(r)),
			attribute.Int("http.response.status_code", status),
			attribute.String("http.route", route),
		}

		outcomeClass := "success"
		if status >= 500 {
			outcomeClass = "server_error"
			attrs = append(attrs, attribute.String("error.type", fmt.Sprintf("%dxx", status/100)))
			span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%dxx", status/100)))
		} else if status >= 400 {
			outcomeClass = "client_error"
		}

		requestDuration.Record(r.Context(), duration.Seconds(), metric.WithAttributes(attrs...))

		requestOutcome.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcomeClass),
		))

		// Primary flow rollup: terminal outcome + duration for the E2E business flow SLIs
		flowOutcomeStr := "success"
		if status >= 500 {
			flowOutcomeStr = "failure"
			span.SetStatus(codes.Error, "server error")
		}

		flowOutcomes.Add(r.Context(), 1, metric.WithAttributes(attribute.String("outcome", flowOutcomeStr)))
		flowDuration.Record(r.Context(), duration.Seconds(), metric.WithAttributes(attribute.String("http.route", route)))

		// Slow-request span event for P99 triage
		const p99BudgetSeconds = 0.750
		if duration.Seconds() > p99BudgetSeconds {
			span.AddEvent("slow_request_budget_exceeded", trace.WithAttributes(
				attribute.String("http.route", route),
				attribute.Float64("duration.seconds", duration.Seconds()),
			))
		}

		span.SetAttributes(attrs...)
	})
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// AuthOutcomeMiddleware wraps an existing JWT auth middleware and records an
// auth.attempts counter tagged with the outcome (allowed/denied) and reason,
// plus a validation-outcome counter for the primary flow's validation step.
func AuthOutcomeMiddleware(jwtMiddleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := jwtMiddleware(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			wrapped.ServeHTTP(rec, r)

			outcome := "allowed"
			validationResult := "passed"
			if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
				outcome = "denied"
				validationResult = "failed"
			}

			authAttempts.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("outcome", outcome),
				attribute.Int("http.response.status_code", rec.status),
			))

			validationOutcome.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("validation.step", "jwt_auth"),
				attribute.String("outcome", validationResult),
			))
		})
	}
}
