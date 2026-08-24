// ----------------------------------------------------------------------------
// OpenTelemetry request telemetry middleware
//
// Emits the semantic-convention http.server.request.duration histogram plus the
// primary business-flow metrics for every request. Must be registered AFTER
// middleware.Recoverer and BEFORE any auth/validation middleware. Requires
// telemetry.Init to have succeeded (fatally enforced in main()) before it is
// wired in, so Tracer/instruments are never nil here.
//
// RequestTelemetryMiddleware is defined on *Base (pkg/api/api.go). ThingAPI
// (cmd/api.go) embeds *api.Base by value, which promotes this (and every other
// *Base method, e.g. SimpleCORSMiddleware) onto ThingAPI - so
// router.Use(api.RequestTelemetryMiddleware) in cmd/server.go resolves correctly.
// ----------------------------------------------------------------------------

package api

import (
	"net/http"
	"time"

	"github.com/benc-uk/go-rest-api/pkg/telemetry"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// slowRequestBudget is the P99 latency budget (750ms); requests exceeding it get a span event.
const slowRequestBudget = 750 * time.Millisecond

// RequestTelemetryMiddleware emits OpenTelemetry traces & metrics for every inbound HTTP request.
func (b *Base) RequestTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		telemetry.IncActiveRequests()
		defer telemetry.DecActiveRequests()

		ctx, span := telemetry.Tracer.Start(r.Context(), "http.request")
		r = r.WithContext(ctx)

		telemetry.FlowEntries.Add(r.Context(), 1)

		// Wrap the response writer so we can read the status code once it's written.
		// chi's WrapResponseWriter preserves Flusher/Hijacker/ReaderFrom on the inner writer.
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		duration := time.Since(start)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		// The route pattern is only populated by chi AFTER routing has taken place.
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unknown"
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLScheme(scheme),
			semconv.HTTPResponseStatusCode(status),
			semconv.HTTPRoute(route),
		}

		// Only 5xx counts as an availability/flow failure, matching the SLI definition
		// count(status<500)/count(total) - 4xx client errors (including auth denials,
		// which are tracked separately via auth.attempts/flow.validation.outcomes) are
		// expected client-side outcomes and must not degrade the flow success SLI.
		outcome := "success"
		if status >= 500 {
			outcome = "error"
			errType := "server_error"
			attrs = append(attrs, attribute.String("error.type", errType))
			span.SetAttributes(attribute.String("error.type", errType))
			span.SetStatus(codes.Error, "server error")
		}

		telemetry.HTTPServerDuration.Record(r.Context(), duration.Seconds(), metric.WithAttributes(attrs...))
		telemetry.FlowOutcomes.Add(r.Context(), 1, metric.WithAttributes(attribute.String("outcome", outcome)))
		telemetry.FlowDuration.Record(r.Context(), duration.Seconds())
		telemetry.FlowEntryToTerminalDuration.Record(r.Context(), duration.Seconds())

		// Slow-request span event for P99 budget breaches, to speed up triage
		if duration > slowRequestBudget {
			span.AddEvent("slow.request", trace.WithAttributes(
				attribute.Float64("duration.seconds", duration.Seconds()),
				semconv.HTTPRoute(route),
			))
		}

		span.SetAttributes(attrs...)
		span.End()
	})
}
