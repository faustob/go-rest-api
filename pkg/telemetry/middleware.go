// ----------------------------------------------------------------------------
// OpenTelemetry HTTP server middleware for the chi router
//
// Latency (http.server.request.duration, seconds) and server spans come from
// the official otelhttp contrib instrumentation. This wrapper adds the chi
// route TEMPLATE, the request outcome counter and slow-request span events.
// ----------------------------------------------------------------------------

package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99Budget is the P99 latency objective; handlers slower than this get a span
// event so the tail can be triaged from the trace.
const p99Budget = 750 * time.Millisecond

// Middleware returns a chi-compatible middleware that emits the OTel HTTP
// server semantic-convention telemetry via otelhttp, enriched with the matched
// chi route template and the SLI outcome counter.
func Middleware(serviceName string) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// otelhttp emits http.server.request.duration (seconds) + the server span
		instrumented := otelhttp.NewHandler(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// chi's wrapper correctly forwards Flusher/Hijacker/Pusher/ReaderFrom
				ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

				start := time.Now()

				if activeRequests != nil {
					activeRequests.Add(r.Context(), 1)
					defer activeRequests.Add(r.Context(), -1)
				}

				// Only records telemetry — never catches or alters the response
				defer func() {
					elapsed := time.Since(start)
					status := ww.Status()

					if status == 0 {
						status = http.StatusOK
					}

					// Route TEMPLATE is only populated AFTER routing has happened
					route := routePattern(r)

					attrs := []attribute.KeyValue{
						attribute.String("http.request.method", r.Method),
						attribute.String("http.route", route),
						attribute.Int("http.response.status_code", status),
						attribute.String("url.scheme", scheme(r)),
						attribute.String("http.response.status_class", statusClass(status)),
					}

					if status >= 400 {
						attrs = append(attrs, attribute.String("error.type", strconv.Itoa(status)))
					}

					if requestCount != nil {
						requestCount.Add(r.Context(), 1, metric.WithAttributes(attrs...))
					}

					// Enrich the otelhttp server span for the metrics -> trace pivot
					span := trace.SpanFromContext(r.Context())
					if !span.IsRecording() {
						return
					}

					span.SetAttributes(
						attribute.String("http.route", route),
						attribute.Int("http.response.status_code", status),
					)

					if status >= 500 {
						// Root-cause class for 5xx attribution
						span.SetAttributes(attribute.String("error.type", strconv.Itoa(status)))
						span.SetStatus(codes.Error, statusClass(status))
					}

					if elapsed > p99Budget {
						span.AddEvent("slow_request", trace.WithAttributes(
							attribute.String("http.route", route),
							attribute.Float64("duration", elapsed.Seconds()),
							attribute.Float64("budget", p99Budget.Seconds()),
						))
					}
				}()

				next.ServeHTTP(ww, r)
			}),
			serviceName,
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return r.Method
			}),
		)

		return instrumented
	}
}

// routePattern returns the matched chi route TEMPLATE (e.g. /things/{id}).
// Never the raw path — that would explode metric cardinality.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}

	return "unmatched"
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	return "http"
}

// statusClass keeps the outcome dimension low cardinality: 2xx, 4xx, 5xx...
func statusClass(status int) string {
	return strconv.Itoa(status/100) + "xx"
}
