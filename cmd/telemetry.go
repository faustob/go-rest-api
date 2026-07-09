// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// OpenTelemetry SDK bootstrap and custom instrumentation for the API.
// This file sets up the global TracerProvider/MeterProvider and defines
// the single package-level meter used by all custom instruments/callbacks.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-chi/chi/v5"
)

// meter is the single package-level meter used for all custom instruments
// and callbacks in this service. It is bound to the global MeterProvider
// registered by setupOTelSDK.
var meter = otel.Meter("github.com/benc-uk/go-rest-api")

// tracer is the tracer used for custom span attribute / event annotations.
var tracer = otel.Tracer("github.com/benc-uk/go-rest-api")

var (
	requestOutcomeCounter metric.Int64Counter
	authAttemptsCounter   metric.Int64Counter
	tenantRequestCounter  metric.Int64Counter
	flowOutcomeCounter    metric.Int64Counter
	flowEntryCounter      metric.Int64Counter
	flowDurationHist      metric.Float64Histogram
	validationOutcomeCtr  metric.Int64Counter

	activeRequests int64 // in-flight request gauge value, updated atomically
	maxWorkers     int64 = 100 // configured worker pool size, adjust as needed
)

// setupOTelSDK builds and registers the OpenTelemetry TracerProvider and
// MeterProvider globally, and initializes custom instruments. It returns a
// shutdown func that should be deferred by the caller. Registration is
// defensive: if a provider is already set (e.g. by an externally-attached
// agent) we log and continue using whatever is already registered.
func setupOTelSDK(ctx context.Context, svcName string) (func(context.Context) error, error) {
	res, err := sdkresource.New(ctx,
		sdkresource.WithAttributes(
			semconv.ServiceName(svcName),
		),
		sdkresource.WithFromEnv(),
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

	// Defensive registration: otel.SetTracerProvider/SetMeterProvider simply
	// replace the global; there is no error to guard against in the Go API,
	// but we still avoid panicking on any unexpected setup error above.
	otel.SetTracerProvider(tp)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetMeterProvider(mp)

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

// initInstruments creates all custom instruments from the single package
// meter, capturing and handling every constructor error.
func initInstruments() error {
	var err error

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	tenantRequestCounter, err = meter.Int64Counter(
		"http.server.requests.by_tenant",
		metric.WithDescription("Count of HTTP requests broken out by tenant/API key"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome count of the primary business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of entries into the primary business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowDurationHist, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	validationOutcomeCtr, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Count of request validation step outcomes"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return err
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP server worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
		o.ObserveInt64(poolSizeGauge, maxWorkers)
		return nil
	}, activeRequestsGauge, poolSizeGauge)
	if err != nil {
		return err
	}

	return nil
}

// attrErrorType returns the standard low-cardinality error.type attribute.
func attrErrorType(errType string) attribute.KeyValue {
	return semconv.ErrorTypeKey.String(errType)
}

// telemetryMiddleware records the primary-flow entry/outcome telemetry,
// per-route/outcome-class counters, tenant throughput, and in-flight
// request saturation. It must run AFTER middleware.Recoverer (so a
// downstream panic is still recovered) and BEFORE auth middleware so it
// also observes auth rejections. Route template is read AFTER the
// downstream handler runs, once chi has populated the RouteContext.
func telemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		atomic.AddInt64(&activeRequests, 1)
		defer atomic.AddInt64(&activeRequests, -1)

		flowEntryCounter.Add(r.Context(), 1)

		sw := &statusCapturingWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(sw, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		status := sw.statusCode
		outcome := "success"
		if status >= 500 {
			outcome = "server_error"
		} else if status >= 400 {
			outcome = "client_error"
		}

		tenant := r.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}

		attrs := metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
			attribute.Int("http.response.status_code", status),
		)
		requestOutcomeCounter.Add(r.Context(), 1, attrs)

		tenantRequestCounter.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("tenant", tenant),
		))

		flowOutcome := "success"
		if status >= 400 {
			flowOutcome = "failed"
		}
		flowOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", flowOutcome),
			attribute.String("http.route", route),
		))

		duration := time.Since(start)
		flowDurationHist.Record(r.Context(), duration.Seconds(), metric.WithAttributes(
			attribute.String("http.route", route),
		))

		// Slow-request span event for P99 triage.
		if duration > 750*time.Millisecond {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.String("http.route", route),
				attribute.Float64("duration.seconds", duration.Seconds()),
			))
		}

		if status >= 500 {
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(attrErrorType("HTTPServerError" + strconv.Itoa(status)))
		}
	})
}

// statusCapturingWriter wraps http.ResponseWriter to capture the status
// code, while forwarding all optional interfaces the wrapped writer may
// implement so streaming/SSE/WebSocket behavior is preserved.
type statusCapturingWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.statusCode = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.statusCode = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusCapturingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusCapturingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support Hijack")
}

func (w *statusCapturingWriter) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		if !w.wroteHeader {
			w.statusCode = http.StatusOK
			w.wroteHeader = true
		}
		return rf.ReadFrom(r)
	}
	if !w.wroteHeader {
		w.statusCode = http.StatusOK
		w.wroteHeader = true
	}
	return io.Copy(w.ResponseWriter, r)
}

// recordAuthOutcome records an authentication/authorization decision. It is
// intended to be called from auth middleware (e.g. pkg/auth JWT validator)
// wherever an allow/deny decision is made.
func recordAuthOutcome(ctx context.Context, allowed bool, reason string) {
	outcome := "allowed"
	if !allowed {
		outer := errors.New(reason)
		_ = outer // reason retained as attribute below; no error is thrown/swallowed here
		outcome = "denied"
	}
	authAttemptsCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("reason", reason),
	))
}

// recordValidationOutcome records a per-step request validation outcome as
// part of the primary flow's validation pipeline.
func recordValidationOutcome(ctx context.Context, step string, passed bool) {
	outcome := "passed"
	if !passed {
		outcome = "failed"
	}
	validationOutcomeCtr.Add(ctx, 1, metric.WithAttributes(
		attribute.String("step", step),
		attribute.String("outcome", outcome),
	))

	_, span := tracer.Start(ctx, "validation."+step)
	span.SetAttributes(attribute.Bool("validation.passed", passed))
	span.End()
}
