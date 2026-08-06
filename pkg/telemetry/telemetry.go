// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation shared by the API.
//
// This is LIBRARY code: it only ever reads the globally registered providers.
// The SDK itself is built and registered by the application entrypoint
// (cmd/otel.go), never here.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/middleware"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope for all telemetry emitted by this repo.
const ScopeName = "github.com/benc-uk/go-rest-api"

// p99Budget is the P99 latency objective for HTTP handlers. Requests slower
// than this get a span event so they can be triaged from the trace.
const p99Budget = 750 * time.Millisecond

// ONE meter for the whole service; every instrument below is created from it.
var meter = otel.Meter(ScopeName)

var (
	requestDuration metric.Float64Histogram
	requestOutcome  metric.Int64Counter
	authAttempts    metric.Int64Counter
)

func init() {
	var err error

	// Standard semconv inbound request duration histogram, in SECONDS.
	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests"),
	)
	if err != nil {
		otel.Handle(err)
	}

	// Request outcome / throughput counter, so availability and request rate are
	// computable without scanning traces.
	requestOutcome, err = meter.Int64Counter(
		"http.server.request.outcome",
		metric.WithUnit("{request}"),
		metric.WithDescription("Inbound HTTP requests broken down by route and outcome class"),
	)
	if err != nil {
		otel.Handle(err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithUnit("{attempt}"),
		metric.WithDescription("Authentication/authorization decisions by outcome and denial reason"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

// RecordAuthAttempt records a single authentication/authorization decision.
// outcome is "allowed" or "denied"; reason is a low-cardinality denial class
// and is omitted when empty.
func RecordAuthAttempt(ctx context.Context, outcome string, reason string) {
	if authAttempts == nil {
		return
	}

	attrs := []attribute.KeyValue{attribute.String("auth.outcome", outcome)}
	if reason != "" {
		attrs = append(attrs, attribute.String("error.type", reason))
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// HTTPMetricsMiddleware records the semantic-convention HTTP server metrics for
// every request, labelled with the MATCHED chi route template (read after the
// downstream handler has run, since routing has not happened on the way in).
//
// It also adds a span event to the active server span when a request exceeds
// the P99 latency budget, and tags 5xx responses with an error class.
func HTTPMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// chi's wrapper correctly forwards Flusher / Hijacker / ReaderFrom,
		// so streaming, SSE and websocket upgrades keep working.
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()

		defer func() {
			elapsed := time.Since(start)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			// Route TEMPLATE (e.g. /things/{id}), never the raw path.
			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}

			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("url.scheme", scheme),
				attribute.Int("http.response.status_code", status),
				attribute.String("network.protocol.version", strconv.Itoa(r.ProtoMajor)+"."+strconv.Itoa(r.ProtoMinor)),
			}
			if route != "" {
				attrs = append(attrs, attribute.String("http.route", route))
			}

			errType := errorTypeForStatus(status)
			if errType != "" {
				attrs = append(attrs, attribute.String("error.type", errType))
			}

			set := metric.WithAttributes(attrs...)

			if requestDuration != nil {
				requestDuration.Record(r.Context(), elapsed.Seconds(), set)
			}

			if requestOutcome != nil {
				outcomeAttrs := append(attrs, attribute.String("http.response.status_class", statusClass(status)))
				requestOutcome.Add(r.Context(), 1, metric.WithAttributes(outcomeAttrs...))
			}

			// Enrich the server span created by otelchi for triage.
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				if errType != "" {
					span.SetAttributes(attribute.String("error.type", errType))
				}

				if elapsed > p99Budget {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.Float64("http.server.request.duration", elapsed.Seconds()),
						attribute.Float64("slo.p99.budget", p99Budget.Seconds()),
						attribute.String("http.route", route),
					))
				}
			}
		}()

		next.ServeHTTP(ww, r)
	})
}

// statusClass reduces a status code to a low-cardinality class, e.g. "2xx".
func statusClass(status int) string {
	return strconv.Itoa(status/100) + "xx"
}

// errorTypeForStatus maps an error response to a low-cardinality error class,
// never a message. Empty for non-error responses.
func errorTypeForStatus(status int) string {
	if status >= 500 {
		return "server_error_" + statusClass(status)
	}

	if status >= 400 {
		return "client_error_" + statusClass(status)
	}

	return ""
}
