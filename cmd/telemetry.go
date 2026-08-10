// ----------------------------------------------------------------------------
// Application telemetry: the single service meter, its instruments, and the
// chi middleware that records HTTP outcome / throughput and auth decisions.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99LatencyBudget is the P99 SLO budget; handlers slower than this get a span event.
const p99LatencyBudget = 750 * time.Millisecond

// ONE meter for the whole service.
var meter = otel.Meter("github.com/benc-uk/go-rest-api")

var (
	requestsTotal    metric.Int64Counter
	requestDuration  metric.Float64Histogram
	activeRequests   metric.Int64UpDownCounter
	authAttempts     metric.Int64Counter
	telemetryEnabled bool
)

func init() {
	var err error

	requestsTotal, err = meter.Int64Counter(
		"http.server.request.outcome",
		metric.WithDescription("Count of inbound HTTP requests by route, status and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️ unable to create http.server.request.outcome counter: %s", err)
		return
	}

	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP server requests"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 2.5, 5, 10),
	)
	if err != nil {
		log.Printf("### ⚠️ unable to create http.server.request.duration histogram: %s", err)
		return
	}

	activeRequests, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️ unable to create http.server.active_requests counter: %s", err)
		return
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome and reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### ⚠️ unable to create auth.attempts counter: %s", err)
		return
	}

	telemetryEnabled = true
}

// statusRecorder captures the response status code while preserving the full
// http.ResponseWriter contract (Flusher, Hijacker, ReaderFrom).
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}

	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}

	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}

	return nil, nil, errors.New("http.Hijacker not supported")
}

func (s *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		if !s.written {
			s.status = http.StatusOK
			s.written = true
		}

		return rf.ReadFrom(r)
	}

	return io.Copy(s.ResponseWriter, r)
}

func outcomeClass(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}

// httpTelemetryMiddleware records request outcome/throughput counters and adds
// slow-request span events. Route template is read AFTER the handler runs.
func httpTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !telemetryEnabled {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		inFlightAttrs := metric.WithAttributes(
			attribute.String("http.request.method", r.Method),
		)
		activeRequests.Add(ctx, 1, inFlightAttrs)

		defer func() {
			elapsed := time.Since(start)

			activeRequests.Add(ctx, -1, inFlightAttrs)

			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}

			if route == "" {
				route = "unmatched"
			}

			class := outcomeClass(rec.status)

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("url.scheme", schemeOf(r)),
				attribute.String("outcome", class),
			}

			if class != "success" {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rec.status)))
			}

			requestsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))

			// Semconv latency distribution (seconds) for P95 / P99
			durationAttrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("url.scheme", schemeOf(r)),
			}

			if class != "success" {
				durationAttrs = append(durationAttrs, attribute.String("error.type", strconv.Itoa(rec.status)))
			}

			requestDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(durationAttrs...))

			// Slow-request span event for P99 triage, plus status/error class on the span.
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				span.SetAttributes(
					attribute.String("http.route", route),
					attribute.Int("http.response.status_code", rec.status),
					attribute.String("outcome", class),
				)

				if class != "success" {
					span.SetAttributes(attribute.String("error.type", strconv.Itoa(rec.status)))
				}

				if elapsed > p99LatencyBudget {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.Float64("duration_s", elapsed.Seconds()),
						attribute.Float64("budget_s", p99LatencyBudget.Seconds()),
						attribute.String("http.route", route),
					))
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// authTelemetryMiddleware records an auth.attempts outcome for every request that
// passes through the protected route group, tagged with the denial reason.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !telemetryEnabled {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusUnauthorized}

		defer func() {
			outcome := "denied"
			reason := "unknown"

			switch {
			case rec.status == http.StatusUnauthorized:
				outcome = "denied"

				if r.Header.Get("Authorization") == "" {
					reason = "missing_credentials"
				} else {
					reason = "invalid_token"
				}
			case rec.status == http.StatusForbidden:
				outcome = "denied"
				reason = "insufficient_scope"
			case rec.status < 400:
				outcome = "allowed"
				reason = "none"
			}

			authAttempts.Add(ctx, 1, metric.WithAttributes(
				attribute.String("outcome", outcome),
				attribute.String("reason", reason),
				attribute.String("http.request.method", r.Method),
			))
		}()

		next.ServeHTTP(rec, r)
	})
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	return "http"
}
