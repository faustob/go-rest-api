// ----------------------------------------------------------------------------
// Custom HTTP telemetry middleware: records the standard OTel semconv
// http.server.request.duration histogram plus a request-outcome counter
// (labeled by route, method, outcome class and tenant), and adds a span
// event when a request exceeds the P99 latency budget (750ms).
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"time"

	"github.com/benc-uk/go-rest-api/pkg/telemetry"
	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// p99Budget is the P99 latency budget used to emit slow-request span events.
const p99Budget = 750 * time.Millisecond

// httpMetricsMiddleware records http.server.request.duration and a
// request-outcome counter for every request handled by the router. Read the
// chi route pattern AFTER next.ServeHTTP returns, since routing hasn't
// happened yet when the middleware is entered.
func httpMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		elapsed := time.Since(start)
		durationSeconds := elapsed.Seconds()

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		status := ww.Status()

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		durationAttrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLScheme(scheme),
			semconv.HTTPRoute(route),
			semconv.HTTPResponseStatusCode(status),
		}

		outcome := "success"
		if status >= 500 {
			outcome = "error"
			durationAttrs = append(durationAttrs, attribute.String("error.type", "server_error"))
		} else if status >= 400 {
			durationAttrs = append(durationAttrs, attribute.String("error.type", "client_error"))
		}

		telemetry.HTTPServerDuration.Record(r.Context(), durationSeconds, otelmetric.WithAttributes(durationAttrs...))

		tenant := r.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}

		telemetry.RequestOutcomeCounter.Add(r.Context(), 1, otelmetric.WithAttributes(
			semconv.HTTPRoute(route),
			semconv.HTTPRequestMethodKey.String(r.Method),
			attribute.String("outcome", outcome),
			attribute.String("tenant.id", tenant),
		))

		if elapsed > p99Budget {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow.request.p99_budget_exceeded", trace.WithAttributes(
				attribute.Float64("http.server.request.duration", durationSeconds),
				semconv.HTTPRoute(route),
				semconv.HTTPRequestMethodKey.String(r.Method),
			))
		}
	})
}
