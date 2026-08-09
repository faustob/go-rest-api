// ----------------------------------------------------------------------------
// OpenTelemetry bootstrap + HTTP server instrumentation for the API server.
//
// This file owns the SINGLE meter/tracer for the service, creates every
// instrument, registers the global SDK in initTelemetry() (called from main)
// and provides the chi middleware that records the SLI signals.
//
// NOTE: this is added ALONGSIDE the existing Prometheus instrumentation.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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

const (
	otelScopeName = "github.com/benc-uk/go-rest-api"

	// P99 latency objective; slower requests get a span event for triage
	slowRequestBudget = 750 * time.Millisecond
)

// ONE meter and ONE tracer for the whole service — every instrument comes from these.
var (
	meter  = otel.Meter(otelScopeName)
	tracer = otel.Tracer(otelScopeName)

	httpServerDuration metric.Float64Histogram
	httpRequestOutcome metric.Int64Counter
	httpActiveRequests metric.Int64UpDownCounter
	authAttempts       metric.Int64Counter

	instrumentsReady bool
)

// initInstruments creates every instrument, handling (never discarding) the errors.
func initInstruments() error {
	var err error

	// Semantic convention: inbound request duration, histogram, SECONDS
	if httpServerDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	); err != nil {
		return err
	}

	// Availability / throughput: request count by route + outcome class attribute
	if httpRequestOutcome, err = meter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("Inbound HTTP requests by route, status code and outcome class"),
	); err != nil {
		return err
	}

	// Goes up and down -> UpDownCounter
	if httpActiveRequests, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
	); err != nil {
		return err
	}

	// Auth failure rate: count(auth.attempts{outcome="denied"}) / count(auth.attempts)
	if authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions by outcome and denial reason"),
	); err != nil {
		return err
	}

	instrumentsReady = true

	return nil
}

// initTelemetry builds the OTel SDK (OTLP/gRPC, endpoint from OTEL_EXPORTER_OTLP_ENDPOINT),
// registers it globally and returns a shutdown function that flushes buffered telemetry.
func initTelemetry(ctx context.Context) (func(context.Context) error, error) {
	noopShutdown := func(context.Context) error { return nil }

	if err := initInstruments(); err != nil {
		return nil, err
	}

	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		log.Print("### 📉 OTEL_SDK_DISABLED=true — no OpenTelemetry SDK registered")

		return noopShutdown, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
		// OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES win over the defaults above
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		if res == nil {
			return nil, err
		}

		log.Printf("### ⚠️ Partial OpenTelemetry resource: %s", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)

		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
	)

	setGlobalProviders(tracerProvider, meterProvider)

	log.Printf("### 📡 OpenTelemetry SDK registered, OTLP endpoint: %q", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))

	return func(shutdownCtx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(shutdownCtx), meterProvider.Shutdown(shutdownCtx))
	}, nil
}

// setGlobalProviders registers defensively: if something else already installed global
// providers we log and carry on rather than crashing the process at startup.
func setGlobalProviders(tracerProvider *sdktrace.TracerProvider, meterProvider *sdkmetric.MeterProvider) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("### ⚠️ OpenTelemetry global providers already registered, continuing: %v", r)
		}
	}()

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// telemetryResponseWriter captures the response status code while forwarding the
// full http.ResponseWriter contract (Flusher, Hijacker, ReaderFrom).
type telemetryResponseWriter struct {
	http.ResponseWriter

	status      int
	wroteHeader bool
}

func (w *telemetryResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}

	w.ResponseWriter.WriteHeader(code)
}

func (w *telemetryResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}

	return w.ResponseWriter.Write(b)
}

func (w *telemetryResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *telemetryResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}

	return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
}

func (w *telemetryResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}

	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}

	return io.Copy(w.ResponseWriter, r)
}

func (w *telemetryResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// telemetryMiddleware records http.server.request.duration (seconds), the request outcome
// counter, in-flight requests, and enriches the server span with error class / slow-request
// events. Route templates are read AFTER next.ServeHTTP, when chi has populated routing.
func telemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !instrumentsReady {
			next.ServeHTTP(w, r)

			return
		}

		start := time.Now()

		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		method := normalizeHTTPMethod(r.Method)
		scheme := "http"

		if r.TLS != nil {
			scheme = "https"
		}

		protoVersion := protocolVersion(r)
		tier := tenantTier(r)

		ctx, span := tracer.Start(ctx, method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(method),
				semconv.URLScheme(scheme),
			),
		)
		defer span.End()

		inflightAttrs := metric.WithAttributes(
			semconv.HTTPRequestMethodKey.String(method),
			semconv.URLScheme(scheme),
		)

		httpActiveRequests.Add(ctx, 1, inflightAttrs)
		defer httpActiveRequests.Add(ctx, -1, inflightAttrs)

		tw := &telemetryResponseWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(tw, r.WithContext(ctx))

		elapsed := time.Since(start)
		status := tw.status

		// chi only knows the matched pattern once routing has happened, i.e. now
		route := ""
		if rc := chi.RouteContext(ctx); rc != nil {
			route = rc.RoutePattern()
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(method),
			semconv.URLScheme(scheme),
			semconv.HTTPResponseStatusCode(status),
			attribute.String("tenant.tier", tier),
		}

		if route != "" {
			attrs = append(attrs, semconv.HTTPRoute(route))
		}

		if protoVersion != "" {
			attrs = append(attrs, semconv.NetworkProtocolVersion(protoVersion))
		}

		if status >= http.StatusInternalServerError {
			attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		}

		httpServerDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))

		outcomeAttrs := make([]attribute.KeyValue, 0, len(attrs)+1)
		outcomeAttrs = append(outcomeAttrs, attrs...)
		outcomeAttrs = append(outcomeAttrs, attribute.String("http.response.status_class", statusClass(status)))

		httpRequestOutcome.Add(ctx, 1, metric.WithAttributes(outcomeAttrs...))

		span.SetAttributes(attrs...)

		if route != "" {
			span.SetName(method + " " + route)
		}

		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, statusClass(status))
		}

		if elapsed > slowRequestBudget {
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.Float64("http.server.request.duration_seconds", elapsed.Seconds()),
				attribute.Float64("slo.latency.budget_seconds", slowRequestBudget.Seconds()),
				attribute.String("http.route", route),
			))
		}
	})
}

// authTelemetryMiddleware counts every authentication/authorization decision. It must be
// registered BEFORE the JWT validator so that short-circuited denials are observed.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !instrumentsReady {
			next.ServeHTTP(w, r)

			return
		}

		tw := &telemetryResponseWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(tw, r)

		outcome, reason := "allowed", "none"

		switch tw.status {
		case http.StatusUnauthorized:
			outcome, reason = "denied", "invalid_or_missing_token"
		case http.StatusForbidden:
			outcome, reason = "denied", "insufficient_scope"
		}

		route := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			route = rc.RoutePattern()
		}

		authAttrs := []attribute.KeyValue{
			attribute.String("outcome", outcome),
			attribute.String("auth.denial_reason", reason),
			semconv.HTTPRequestMethodKey.String(normalizeHTTPMethod(r.Method)),
		}

		if route != "" {
			authAttrs = append(authAttrs, semconv.HTTPRoute(route))
		}

		authAttempts.Add(r.Context(), 1, metric.WithAttributes(authAttrs...))

		if outcome == "denied" {
			trace.SpanFromContext(r.Context()).SetAttributes(
				attribute.String("outcome", outcome),
				attribute.String("auth.denial_reason", reason),
			)
		}
	})
}

// normalizeHTTPMethod keeps the method dimension bounded, per semantic conventions.
func normalizeHTTPMethod(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return strings.ToUpper(method)
	default:
		return "_OTHER"
	}
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

func protocolVersion(r *http.Request) string {
	switch {
	case r.ProtoMajor == 1 && r.ProtoMinor == 1:
		return "1.1"
	case r.ProtoMajor == 1 && r.ProtoMinor == 0:
		return "1.0"
	case r.ProtoMajor == 2:
		return "2"
	case r.ProtoMajor == 3:
		return "3"
	default:
		return ""
	}
}

// tenantTier is the business cohort dimension for latency/throughput SLOs. It is
// deliberately restricted to a fixed allow-list to keep cardinality bounded.
func tenantTier(r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Tenant-Tier"))) {
	case "free":
		return "free"
	case "standard":
		return "standard"
	case "premium":
		return "premium"
	case "enterprise":
		return "enterprise"
	default:
		return "unknown"
	}
}
