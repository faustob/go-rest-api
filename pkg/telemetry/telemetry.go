// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// OpenTelemetry SDK bootstrap and HTTP/auth telemetry middleware
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

var (
	serviceMeter  metric.Meter
	serviceTracer trace.Tracer

	globalTracerProvider trace.TracerProvider
	globalMeterProvider  metric.MeterProvider

	// Instruments
	httpRequestDuration  metric.Float64Histogram
	httpRequestsOutcome  metric.Int64Counter
	httpActiveRequests   int64
	httpActiveRequestsGa metric.Int64ObservableGauge
	httpWorkerPoolSizeGa metric.Int64ObservableGauge
	authAttemptsCounter  metric.Int64Counter
	flowOutcomeCounter   metric.Int64Counter
	flowEntryCounter     metric.Int64Counter
	flowDurationHist     metric.Float64Histogram
	validationOutcomeCtr metric.Int64Counter

	maxWorkers = 1000 // configured worker pool size, adjust to actual server capacity
)

// SetupOTelSDK builds and registers the OpenTelemetry TracerProvider and MeterProvider as globals.
// Returns a shutdown function that should be deferred by the caller.
func SetupOTelSDK(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	tp := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(traceExporter),
		tracesdk.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
	)

	// Defensive registration: tolerate an already-registered global provider (e.g. from an agent)
	func() {
		defer func() {
			if r := recover(); r != nil {
				_ = r // already set, keep going
			}
		}()
		otel.SetTracerProvider(tp)
	}()

	func() {
		defer func() {
			if r := recover(); r != nil {
				_ = r // already set, keep going
			}
		}()
		otel.SetMeterProvider(mp)
	}()

	globalTracerProvider = otel.GetTracerProvider()
	globalMeterProvider = otel.GetMeterProvider()

	serviceTracer = globalTracerProvider.Tracer("go-rest-api")
	serviceMeter = globalMeterProvider.Meter("go-rest-api")

	if err := initInstruments(); err != nil {
		return nil, err
	}

	_ = endpoint // endpoint is consumed by otlptracegrpc via OTEL_EXPORTER_OTLP_ENDPOINT env var

	shutdown := func(ctx context.Context) error {
		var errs []error
		if err := tp.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		if err := mp.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}

	return shutdown, nil
}

func initInstruments() error {
	var err error

	httpRequestDuration, err = serviceMeter.Float64Histogram(
		"http.server.custom.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests (custom, business-dimension attributes; distinct from otelhttp's built-in http.server.request.duration)"),
	)
	if err != nil {
		return err
	}

	httpRequestsOutcome, err = serviceMeter.Int64Counter(
		"http.server.request.outcome.total",
		metric.WithUnit("{request}"),
		metric.WithDescription("Count of HTTP requests labeled by route and outcome class"),
	)
	if err != nil {
		return err
	}

	httpActiveRequestsGa, err = serviceMeter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("Number of in-flight HTTP requests"),
	)
	if err != nil {
		return err
	}

	httpWorkerPoolSizeGa, err = serviceMeter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithUnit("{worker}"),
		metric.WithDescription("Configured worker pool size"),
	)
	if err != nil {
		return err
	}

	_, err = serviceMeter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(httpActiveRequestsGa, atomic.LoadInt64(&httpActiveRequests))
		o.ObserveInt64(httpWorkerPoolSizeGa, int64(maxWorkers))
		return nil
	}, httpActiveRequestsGa, httpWorkerPoolSizeGa)
	if err != nil {
		return err
	}

	authAttemptsCounter, err = serviceMeter.Int64Counter(
		"auth.attempts.total",
		metric.WithUnit("{attempt}"),
		metric.WithDescription("Count of authentication/authorization decisions, tagged with outcome and reason"),
	)
	if err != nil {
		return err
	}

	flowOutcomeCounter, err = serviceMeter.Int64Counter(
		"flow.outcomes.total",
		metric.WithUnit("{flow}"),
		metric.WithDescription("Terminal outcome count of the end-to-end business flow"),
	)
	if err != nil {
		return err
	}

	flowEntryCounter, err = serviceMeter.Int64Counter(
		"flow.entries.total",
		metric.WithUnit("{flow}"),
		metric.WithDescription("Count of entries into the primary business flow"),
	)
	if err != nil {
		return err
	}

	flowDurationHist, err = serviceMeter.Float64Histogram(
		"flow.duration",
		metric.WithUnit("s"),
		metric.WithDescription("End-to-end duration of the primary business flow, entry to terminal state"),
	)
	if err != nil {
		return err
	}

	validationOutcomeCtr, err = serviceMeter.Int64Counter(
		"flow.validation.outcomes.total",
		metric.WithUnit("{validation}"),
		metric.WithDescription("Count of request validation attempts by outcome"),
	)
	if err != nil {
		return err
	}

	return nil
}

// TracerProvider returns the registered global tracer provider (for otelhttp middleware wiring)
func TracerProvider() trace.TracerProvider {
	if globalTracerProvider == nil {
		return otel.GetTracerProvider()
	}
	return globalTracerProvider
}

// MeterProvider returns the registered global meter provider (for otelhttp middleware wiring)
func MeterProvider() metric.MeterProvider {
	if globalMeterProvider == nil {
		return otel.GetMeterProvider()
	}
	return globalMeterProvider
}

// statusRecorder wraps http.ResponseWriter to capture the status code while preserving
// the optional interfaces (Flush, Hijack, ReadFrom) the underlying writer may implement.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack preserves the http.Hijacker interface of the underlying ResponseWriter,
// required for WebSocket/upgrade handlers to function when wrapped.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support http.Hijacker")
}

// ReadFrom preserves the io.ReaderFrom interface of the underlying ResponseWriter,
// required for sendfile-style optimizations to function when wrapped.
func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// HTTPTelemetryMiddleware records request outcome counters, route-level latency (business dimension),
// slow-request span events (P99 breakdown), and drives active-request gauge accounting.
func HTTPTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		atomic.AddInt64(&httpActiveRequests, 1)
		defer atomic.AddInt64(&httpActiveRequests, -1)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Entry counter for the primary business flow (every inbound request is a flow entry)
		if flowEntryCounter != nil {
			flowEntryCounter.Add(r.Context(), 1)
		}

		span := trace.SpanFromContext(r.Context())

		next.ServeHTTP(rec, r)

		duration := time.Since(start)

		route := routeTemplate(r)
		tenantTier := tenantTierOf(r)

		outcome := "success"
		if rec.status >= 500 {
			outer := "server_error"
			outer = outer // no-op to keep readability
			outcome = "server_error"
		} else if rec.status >= 400 {
			outcome = "client_error"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", rec.status),
			attribute.String("url.scheme", schemeOf(r)),
		}

		if tenantTier != "" {
			attrs = append(attrs, attribute.String("tenant.tier", tenantTier))
		}

		if rec.status >= 400 {
			attrs = append(attrs, attribute.String("error.type", errorClass(rec.status)))
		}

		if httpRequestDuration != nil {
			httpRequestDuration.Record(r.Context(), duration.Seconds(), metric.WithAttributes(attrs...))
		}

		if httpRequestsOutcome != nil {
			outcomeAttrs := append([]attribute.KeyValue{attribute.String("outcome", outcome)}, attrs...)
			httpRequestsOutcome.Add(r.Context(), 1, metric.WithAttributes(outcomeAttrs...))
		}

		// Flow outcome (terminal state of the primary business flow) & duration
		flowOutcome := "success"
		if rec.status >= 400 {
			flowOutcome = "failed"
		}
		if flowOutcomeCounter != nil {
			flowOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(attribute.String("outcome", flowOutcome)))
		}
		if flowDurationHist != nil {
			flowDurationHist.Record(r.Context(), duration.Seconds(), metric.WithAttributes(attribute.String("outcome", flowOutcome)))
		}

		// P99 slow-request span event with breakdown, budget = 750ms per SLI
		const p99Budget = 750 * time.Millisecond
		if duration > p99Budget && span.IsRecording() {
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.String("http.route", route),
				attribute.Float64("duration.seconds", duration.Seconds()),
				attribute.Float64("p99_budget.seconds", p99Budget.Seconds()),
			))
		}

		// Attribute exception/status class on the span for 5xx root-cause attribution
		if rec.status >= 500 {
			span.SetAttributes(attribute.String("error.type", errorClass(rec.status)))
		}
	})
}

// AuthOutcomeMiddleware wraps an existing auth middleware (e.g. JWT validator) to emit an
// auth attempt outcome counter. It does not alter control flow: it observes the response
// status the wrapped middleware produces to classify allowed vs denied.
func AuthOutcomeMiddleware(authMiddleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := authMiddleware(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			wrapped.ServeHTTP(rec, r)

			outcome := "allowed"
			reason := "none"
			if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
				outcome = "denied"
				reason = strconv.Itoa(rec.status)
			}

			if authAttemptsCounter != nil {
				authAttemptsCounter.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("outcome", outcome),
					attribute.String("reason", reason),
				))
			}

			validationOutcome := "passed"
			if outcome == "denied" {
				validationOutcome = "failed"
			}
			if validationOutcomeCtr != nil {
				validationOutcomeCtr.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("step", "jwt_auth"),
					attribute.String("outcome", validationOutcome),
				))
			}
		})
	}
}

func routeTemplate(r *http.Request) string {
	if rc := getChiRouteContext(r); rc != "" {
		return rc
	}
	return "unknown"
}

// getChiRouteContext attempts to read the matched chi route pattern from the request context.
func getChiRouteContext(r *http.Request) string {
	if rctx := chiRouteCtx(r); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func tenantTierOf(r *http.Request) string {
	return r.Header.Get("X-Tenant-Tier")
}

func errorClass(status int) string {
	return fmt.Sprintf("http_%dxx", status/100)
}

// avoid unused import issues for otelhttp when only referenced via server.go wiring
var _ = otelhttp.WithMeterProvider
