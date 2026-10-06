// ----------------------------------------------------------------------------
// HTTP server telemetry: request duration/outcome/throughput metrics and an
// auth-attempt outcome counter, wired into the middleware chain in server.go.
// One meter & tracer for the whole service; every instrument is created here.
// ----------------------------------------------------------------------------

package main

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const telemetryScope = "github.com/benc-uk/go-rest-api"

// Single meter & tracer for the whole service - every instrument and span
// below is created from these, never per-file.
var (
	meter  = otel.Meter(telemetryScope)
	tracer = otel.Tracer(telemetryScope)
)

// slowRequestThreshold is the P99 latency budget; crossing it emits a span event.
const slowRequestThreshold = 750 * time.Millisecond

var (
	httpRequestDuration metric.Float64Histogram
	httpRequestCounter  metric.Int64Counter
	authAttemptsCounter metric.Int64Counter
)

func init() {
	var err error

	httpRequestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Fatalf("### ❌ failed to create http.server.request.duration histogram: %v", err)
	}

	httpRequestCounter, err = meter.Int64Counter(
		"http.server.requests",
		metric.WithDescription("Count of inbound HTTP requests by route, outcome and tenant"),
	)
	if err != nil {
		log.Fatalf("### ❌ failed to create http.server.requests counter: %v", err)
	}

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
	)
	if err != nil {
		log.Fatalf("### ❌ failed to create auth.attempts counter: %v", err)
	}
}

// requestMetricsMiddleware records http.server.request.duration (tagged with
// a tenant-tier business dimension), the http.server.requests outcome/tenant
// counter, slow-request span events for P99 triage, and the originating
// status's error class as a span attribute on 5xx responses.
func requestMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		start := time.Now()

		ctx, span := tracer.Start(req.Context(), "http.request")
		req = req.WithContext(ctx)

		ww := middleware.NewWrapResponseWriter(resp, req.ProtoMajor)

		next.ServeHTTP(ww, req)

		duration := time.Since(start)
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		route := chi.RouteContext(req.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}

		tier := req.Header.Get("X-Tenant-Tier")
		if tier == "" {
			tier = "unknown"
		}

		outcome := "success"
		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(req.Method),
			semconv.URLScheme(scheme),
			semconv.HTTPResponseStatusCode(status),
			semconv.HTTPRoute(route),
			attribute.String("tenant.tier", tier),
		}

		if status >= 500 {
			outcome = "error"
			errType := http.StatusText(status)
			attrs = append(attrs, semconv.ErrorTypeKey.String(errType))
			span.SetAttributes(semconv.ErrorTypeKey.String(errType))
			span.SetStatus(codes.Error, errType)
		}

		httpRequestDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))

		tenant := req.Header.Get("X-Tenant-ID")
		if tenant == "" {
			tenant = "unknown"
		}

		countAttrs := append(append([]attribute.KeyValue{}, attrs...),
			attribute.String("outcome", outcome),
			attribute.String("tenant.id", tenant),
		)
		httpRequestCounter.Add(ctx, 1, metric.WithAttributes(countAttrs...))

		if duration >= slowRequestThreshold {
			span.AddEvent("slow.request", trace.WithAttributes(
				attribute.Float64("http.server.request.duration", duration.Seconds()),
				semconv.HTTPRoute(route),
			))
		}

		span.End()
	})
}

// authOutcomeMiddleware records auth.attempts, observing the status the JWT
// validator it wraps produces (401/403 = denied, anything else = granted).
// It is registered BEFORE the JWT validator so it also sees rejections.
func authOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ww := middleware.NewWrapResponseWriter(resp, req.ProtoMajor)

		next.ServeHTTP(ww, req)

		status := ww.Status()
		outcome := "granted"
		reason := "none"
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			outcome = "denied"
			reason = http.StatusText(status)
		}

		authAttemptsCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}
