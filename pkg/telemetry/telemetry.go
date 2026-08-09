// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation helpers for the API
//
// This package is LIBRARY code, it never builds or registers an SDK. It uses
// the global provider registered by the application at startup (see cmd/otel.go)
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/middleware"
	chi5 "github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope for all telemetry from this service
const ScopeName = "github.com/benc-uk/go-rest-api"

// P99LatencyBudget is the P99 SLO budget, requests slower than this get a span event
const P99LatencyBudget = 750 * time.Millisecond

// ONE meter & tracer per service, every instrument below is created from these
var (
	meter  = otel.Meter(ScopeName)
	tracer = otel.Tracer(ScopeName)
)

var (
	// requestDuration is the OTel semantic convention inbound request duration histogram (SECONDS)
	requestDuration metric.Float64Histogram
	// activeRequests goes up and down, so it must be an UpDownCounter
	activeRequests metric.Int64UpDownCounter
	// authAttempts counts every authentication/authorization decision
	authAttempts metric.Int64Counter
)

func init() {
	var err error

	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP server requests"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create http.server.request.duration: %s", err)
	}

	activeRequests, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create http.server.active_requests: %s", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithUnit("{attempt}"),
		metric.WithDescription("Authentication & authorization decisions, by outcome"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create auth.attempts: %s", err)
	}
}

// HTTPMiddleware records OTel semantic convention HTTP server metrics and a server span.
// Register it with router.Use() AFTER middleware.Recoverer and BEFORE any auth middleware.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		baseAttrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", scheme),
			attribute.String("network.protocol.version", protocolVersion(r)),
		}

		// The span context is DERIVED from the incoming request context, so everything
		// already in it (including chi's *RouteContext) is preserved downstream
		ctx, span := tracer.Start(r.Context(), r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(baseAttrs...),
		)
		defer span.End()

		if activeRequests != nil {
			activeRequests.Add(ctx, 1, metric.WithAttributes(baseAttrs...))
			defer activeRequests.Add(ctx, -1, metric.WithAttributes(baseAttrs...))
		}

		// chi's wrapper preserves Flusher / Hijacker / Pusher / ReaderFrom of the original writer
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		// tracedReq is the request actually served downstream, its context is the one
		// chi routes with, so the matched route template must be read back from IT
		tracedReq := r.WithContext(ctx)
		start := time.Now()

		// The route pattern is only populated by chi AFTER routing, so it is read below
		defer func() {
			elapsed := time.Since(start)
			status := ww.Status()

			if status == 0 {
				status = http.StatusOK
			}

			attrs := append([]attribute.KeyValue{}, baseAttrs...)
			attrs = append(attrs, attribute.Int("http.response.status_code", status))

			// Low cardinality route TEMPLATE (e.g. /things/{id}), never the raw path
			route := routePattern(tracedReq)
			if route == "" {
				// Fall back to the original request in case a handler replaced the context
				route = routePattern(r)
			}

			if route != "" {
				attrs = append(attrs, attribute.String("http.route", route))
				span.SetName(r.Method + " " + route)
			}

			// error.type is the status CLASS for server errors, never a message
			if status >= 500 {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(status)))
			}

			span.SetAttributes(attrs...)

			if requestDuration != nil {
				requestDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))
			}

			// Slow request span event for P99 triage
			if elapsed > P99LatencyBudget {
				span.AddEvent("slow_request", trace.WithAttributes(
					attribute.Float64("duration_s", elapsed.Seconds()),
					attribute.Float64("budget_s", P99LatencyBudget.Seconds()),
				))
			}
		}()

		next.ServeHTTP(ww, tracedReq)
	})
}

// RecordAuthOutcome records one authentication/authorization decision.
// outcome is "allowed" or "denied", reason is a low cardinality denial class (may be empty)
func RecordAuthOutcome(ctx context.Context, outcome string, reason string) {
	if authAttempts == nil {
		return
	}

	attrs := []attribute.KeyValue{attribute.String("outcome", outcome)}
	if reason != "" {
		attrs = append(attrs, attribute.String("error.type", reason))
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// routePattern returns the matched chi route template, empty if there is no match
func routePattern(r *http.Request) string {
	if r == nil {
		return ""
	}

	if rctx := chi5.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}

	return ""
}

// protocolVersion maps the request protocol to the semconv network.protocol.version value
func protocolVersion(r *http.Request) string {
	switch r.ProtoMajor {
	case 1:
		if r.ProtoMinor == 0 {
			return "1.0"
		}

		return "1.1"
	case 2:
		return "2"
	case 3:
		return "3"
	}

	return strconv.Itoa(r.ProtoMajor)
}
