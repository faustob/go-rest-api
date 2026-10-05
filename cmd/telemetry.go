// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// OpenTelemetry bootstrap: builds and registers the global TracerProvider and
// MeterProvider (OTLP/gRPC), and provides HTTP server telemetry middleware.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	meter otelmetric.Meter

	httpRequestDuration  otelmetric.Float64Histogram
	httpRequestOutcome   otelmetric.Int64Counter
	authAttemptOutcome   otelmetric.Int64Counter
	httpRequestsByTenant otelmetric.Int64Counter
)

// initTelemetry builds and registers the global OTel TracerProvider and
// MeterProvider, exporting via OTLP/gRPC (endpoint driven by the standard
// OTEL_EXPORTER_OTLP_ENDPOINT env var). It returns a shutdown function that
// the caller must defer to flush buffered spans/metrics on exit.
func initTelemetry(ctx context.Context) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	meter = otel.Meter(serviceName)

	httpRequestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		otelmetric.WithDescription("Duration of inbound HTTP requests"),
		otelmetric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	httpRequestOutcome, err = meter.Int64Counter(
		"http.server.request.outcome",
		otelmetric.WithDescription("Count of HTTP requests labeled by route and outcome class (success, client_error, server_error)"),
		otelmetric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	authAttemptOutcome, err = meter.Int64Counter(
		"auth.attempts",
		otelmetric.WithDescription("Count of authentication/authorization decisions, tagged with the outcome and denial reason"),
		otelmetric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, err
	}

	httpRequestsByTenant, err = meter.Int64Counter(
		"http.server.requests_by_tenant",
		otelmetric.WithDescription("Count of HTTP requests labeled by tenant/API key"),
		otelmetric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	return func(shutdownCtx context.Context) error {
		if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return meterProvider.Shutdown(shutdownCtx)
	}, nil
}

// schemeFor returns the request URL scheme (http/https) in a low-cardinality way.
func schemeFor(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if r.URL != nil && r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	return "http"
}

// httpTelemetryMiddleware records the OTel semantic-convention HTTP server
// request duration histogram (http.server.request.duration) plus a
// route+outcome counter and a per-tenant request counter. It also annotates
// the active span (created by the otelchi tracing middleware registered
// ahead of it) with an error.type attribute on 5xx responses and a
// slow-request span event when the P99 latency budget (750ms) is exceeded.
func httpTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		duration := time.Since(start).Seconds()

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unknown"
		}

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLScheme(schemeFor(r)),
			semconv.HTTPRoute(route),
			semconv.HTTPResponseStatusCode(status),
		}

		span := trace.SpanFromContext(r.Context())

		outcome := "success"
		switch {
		case status >= 500:
			outcome = "server_error"
			attrs = append(attrs, semconv.ErrorTypeKey.String("internal_server_error"))
			span.SetAttributes(semconv.ErrorTypeKey.String("internal_server_error"))
			span.SetStatus(codes.Error, "internal server error")
		case status >= 400:
			outcome = "client_error"
		}

		// Slow-request budget: P99 target is 750ms (see go-rest-api-http-latency-p99 SLI).
		if duration > 0.75 {
			span.AddEvent("slow.request", trace.WithAttributes(
				attribute.Float64("duration.seconds", duration),
				semconv.HTTPRoute(route),
			))
		}

		httpRequestDuration.Record(r.Context(), duration, otelmetric.WithAttributes(attrs...))

		httpRequestOutcome.Add(r.Context(), 1, otelmetric.WithAttributes(
			semconv.HTTPRoute(route),
			attribute.String("outcome", outcome),
		))

		tenant := r.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}
		httpRequestsByTenant.Add(r.Context(), 1, otelmetric.WithAttributes(
			attribute.String("tenant", tenant),
			semconv.HTTPRoute(route),
		))
	})
}

// authOutcomeMiddleware records every authentication/authorization decision
// observed for the protected route group. It must be registered BEFORE the
// JWT validator middleware so it also observes the validator's own 401/403
// rejections, not just downstream handler errors.
func authOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		status := ww.Status()
		outcome := "allowed"
		reason := "n/a"
		switch status {
		case http.StatusUnauthorized:
			outcome = "denied"
			reason = "unauthorized"
		case http.StatusForbidden:
			outcome = "denied"
			reason = "forbidden"
		}

		authAttemptOutcome.Add(r.Context(), 1, otelmetric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}
