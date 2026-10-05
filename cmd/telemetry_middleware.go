// ----------------------------------------------------------------------------
// Custom telemetry middleware: HTTP request duration/outcome/per-tenant
// metrics and auth attempt outcomes, recorded from the single package-level
// meter declared in telemetry.go.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99BudgetMillis is the HTTP Response Time P99 budget (750ms) used to flag
// slow requests with a span event for triage (see go-rest-api-http-latency-p99).
const p99BudgetMillis = 750

// RequestOutcomeMiddleware records the http.server.request.duration
// histogram, a request-outcome counter and a per-tenant request counter for
// every request, and adds a slow-request span event when a request exceeds
// the P99 latency budget. It MUST be registered after middleware.Recoverer
// (so a downstream panic is still recovered) so outcomes reflect the final
// response status, and before any auth/validation middleware whose
// rejections also need to be counted by the route-level metrics.
func RequestOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		start := time.Now()

		ww, ok := resp.(middleware.WrapResponseWriter)
		if !ok {
			ww = middleware.NewWrapResponseWriter(resp, req.ProtoMajor)
		}

		next.ServeHTTP(ww, req)

		duration := time.Since(start)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		// The matched route pattern is only populated AFTER routing has
		// happened, i.e. after next.ServeHTTP has returned.
		route := req.URL.Path
		if rctx := chi.RouteContext(req.Context()); rctx != nil {
			if pattern := rctx.RoutePattern(); pattern != "" {
				route = pattern
			}
		}

		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}

		outcome := "success"
		if status >= 500 {
			outcome = "error"
		} else if status >= 400 {
			outcome = "client_error"
		}

		durationAttrs := []attribute.KeyValue{
			attribute.String("http.request.method", req.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
			attribute.String("url.scheme", scheme),
		}
		if status >= 500 {
			durationAttrs = append(durationAttrs, attribute.String("error.type", "5xx"))
		}

		httpServerDurationHistogram.Record(req.Context(), duration.Seconds(), metric.WithAttributes(durationAttrs...))

		httpRequestOutcomeCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
		))

		tenant := req.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}
		httpRequestsByTenantCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("tenant", tenant),
		))

		if duration.Milliseconds() > p99BudgetMillis {
			span := trace.SpanFromContext(req.Context())
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.String("http.route", route),
				attribute.Int64("duration_ms", duration.Milliseconds()),
			))
		}

		if status >= 500 {
			span := trace.SpanFromContext(req.Context())
			span.SetAttributes(attribute.String("error.type", "5xx"))
		}
	})
}

// AuthOutcomeMiddleware records an auth attempt outcome counter for every
// request in the protected route group. It MUST be registered BEFORE
// jwtValidator.Middleware so it observes denied requests, which the
// validator short-circuits before calling next.
func AuthOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ww, ok := resp.(middleware.WrapResponseWriter)
		if !ok {
			ww = middleware.NewWrapResponseWriter(resp, req.ProtoMajor)
		}

		next.ServeHTTP(ww, req)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		outcome := "allowed"
		reason := "none"
		switch status {
		case http.StatusUnauthorized:
			outcome = "denied"
			reason = "unauthorized"
		case http.StatusForbidden:
			outcome = "denied"
			reason = "forbidden"
		}

		authAttemptCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}
