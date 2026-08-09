// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation for the REST API: HTTP server metrics/spans and
// authentication outcome metrics.
//
// This package only uses the GLOBAL OpenTelemetry providers; the SDK itself is
// built and registered by the application entrypoint (see cmd/otel.go).
// ----------------------------------------------------------------------------

package telemetry

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	// ScopeName is the single instrumentation scope used by this service.
	ScopeName = "github.com/benc-uk/go-rest-api"

	// P99LatencyBudget is the P99 latency SLO budget for HTTP handlers.
	P99LatencyBudget = 750 * time.Millisecond

	// tenantTierHeader carries the (low cardinality) business dimension for latency cohorts.
	tenantTierHeader = "X-Tenant-Tier"
)

// ONE meter and ONE tracer for the whole service, every instrument comes from them.
var (
	meter  = otel.Meter(ScopeName)
	tracer = otel.Tracer(ScopeName)

	requestDuration metric.Float64Histogram
	requestsTotal   metric.Int64Counter
	activeRequests  metric.Int64UpDownCounter
	authAttempts    metric.Int64Counter
)

func init() {
	var err error

	if requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP server requests"),
	); err != nil {
		otel.Handle(err)
	}

	if requestsTotal, err = meter.Int64Counter(
		"http.server.requests",
		metric.WithDescription("Count of inbound HTTP server requests by route, tenant tier and outcome class"),
	); err != nil {
		otel.Handle(err)
	}

	if activeRequests, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP server requests"),
	); err != nil {
		otel.Handle(err)
	}

	if authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions by outcome and denial reason"),
	); err != nil {
		otel.Handle(err)
	}
}

// Middleware records the semantic convention HTTP server duration histogram, a
// request outcome counter, in-flight requests and a server span for every
// inbound request. Register it AFTER middleware.Recoverer and BEFORE any auth
// middleware, so short-circuited (denied) requests are observed too.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), r.Method, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		// Thread the span context through so downstream spans nest correctly
		r = r.WithContext(ctx)

		// chi's wrapper preserves Flush/Hijack/ReadFrom/Push of the original writer
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		method := r.Method
		scheme := urlScheme(r)
		proto := protocolVersion(r)
		tier := tenantTier(r)
		start := time.Now()

		inflightAttrs := metric.WithAttributes(
			semconv.HTTPRequestMethodKey.String(method),
			semconv.URLScheme(scheme),
		)
		activeRequests.Add(ctx, 1, inflightAttrs)

		defer func() {
			elapsed := time.Since(start)
			activeRequests.Add(ctx, -1, inflightAttrs)

			// The chi route pattern is only populated once routing has happened, so read it here
			route := routePattern(r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			attrs := []attribute.KeyValue{
				semconv.HTTPRequestMethodKey.String(method),
				semconv.URLScheme(scheme),
				semconv.HTTPRoute(route),
				semconv.HTTPResponseStatusCode(status),
				semconv.NetworkProtocolVersion(proto),
				attribute.String("tenant.tier", tier),
			}

			if status >= 500 {
				attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
			}

			requestDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))

			outcomeAttrs := make([]attribute.KeyValue, 0, len(attrs)+1)
			outcomeAttrs = append(outcomeAttrs, attrs...)
			outcomeAttrs = append(outcomeAttrs, attribute.String("http.response.status_class", statusClass(status)))
			requestsTotal.Add(ctx, 1, metric.WithAttributes(outcomeAttrs...))

			span.SetName(method + " " + route)
			span.SetAttributes(attrs...)

			if status >= 500 {
				span.SetStatus(codes.Error, statusClass(status))
			}

			if elapsed > P99LatencyBudget {
				span.AddEvent("slow_request", trace.WithAttributes(
					attribute.Float64("http.server.request.duration", elapsed.Seconds()),
					attribute.Float64("slo.budget", P99LatencyBudget.Seconds()),
					semconv.HTTPRoute(route),
					attribute.String("tenant.tier", tier),
				))
			}
		}()

		next.ServeHTTP(ww, r)
	})
}

// AuthMiddleware counts every authentication/authorization decision, tagged with
// the outcome and the denial reason. It must be registered BEFORE (so that it
// wraps) the JWT validator, otherwise denied requests are never observed.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		outcome := "granted"
		reason := "none"

		switch ww.Status() {
		case http.StatusUnauthorized:
			outcome, reason = "denied", "invalid_token"
		case http.StatusForbidden:
			outcome, reason = "denied", "insufficient_scope"
		}

		authAttempts.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("auth.failure.reason", reason),
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.HTTPRoute(routePattern(r)),
		))
	})
}

func urlScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	return "http"
}

func protocolVersion(r *http.Request) string {
	return fmt.Sprintf("%d.%d", r.ProtoMajor, r.ProtoMinor)
}

// routePattern returns the matched chi route TEMPLATE (never the raw path) to
// keep metric cardinality bounded.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}

	return "unmatched"
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// tenantTier maps the tenant tier header onto a small allow-list, so an
// arbitrary client header can never explode metric cardinality.
func tenantTier(r *http.Request) string {
	switch strings.ToLower(r.Header.Get(tenantTierHeader)) {
	case "free":
		return "free"
	case "standard":
		return "standard"
	case "premium":
		return "premium"
	default:
		return "unknown"
	}
}
