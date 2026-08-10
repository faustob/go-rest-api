// ----------------------------------------------------------------------------
// Application-level HTTP telemetry: route-level outcome, throughput, latency
// and authentication-decision metrics.
//
// A single meter is declared here for the whole service and every instrument
// is created from it.
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

// One meter for the whole service
var meter = otel.Meter("github.com/benc-uk/go-rest-api")

var (
	// Request outcome / throughput counter (availability, error-rate, request-rate SLIs)
	requestCounter metric.Int64Counter

	// Server-side request latency in SECONDS (p95 / p99 SLIs)
	requestDuration metric.Float64Histogram

	// Authentication / authorization decision outcomes (auth-failure-rate SLI)
	authAttempts metric.Int64Counter
)

// p99Budget is the latency budget above which a span event is emitted for triage
const p99Budget = 750 * time.Millisecond

func init() {
	var err error

	requestCounter, err = meter.Int64Counter(
		"http.server.request",
		metric.WithDescription("Total inbound HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create http.server.request counter: %s", err)
	}

	requestDuration, err = meter.Float64Histogram(
		"http.server.route.duration",
		metric.WithDescription("Duration of inbound HTTP requests by matched route"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create http.server.route.duration histogram: %s", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create auth.attempts counter: %s", err)
	}
}

// statusRecorder captures the response status code while forwarding the full
// http.ResponseWriter contract (Flush / Hijack / ReadFrom).
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

	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}

	return io.Copy(s.ResponseWriter, r)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// outcomeClass maps a status code to a low cardinality outcome class
func outcomeClass(status int) string {
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

// httpTelemetryMiddleware records request outcome, throughput, latency and
// authentication decisions. It must be registered after middleware.Recoverer
// and before the JWT validator so denied requests are still observed.
func httpTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		defer func() {
			elapsed := time.Since(start)

			// Route TEMPLATE is only populated once routing has happened
			route := "unmatched"
			if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
				route = rctx.RoutePattern()
			}

			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}

			class := outcomeClass(rec.status)

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
				attribute.String("url.scheme", scheme),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("network.protocol.version", protoVersion(r.Proto)),
			}

			if rec.status >= 400 {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rec.status)))
			}

			if requestDuration != nil {
				requestDuration.Record(r.Context(), elapsed.Seconds(), metric.WithAttributes(attrs...))
			}

			if requestCounter != nil {
				requestCounter.Add(r.Context(), 1, metric.WithAttributes(
					append(attrs, attribute.String("http.response.status_class", class))...,
				))
			}

			// Authentication/authorization decision outcome
			if authAttempts != nil {
				if _, hasAuth := r.Header["Authorization"]; hasAuth || rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
					outcome := "allowed"
					reason := "none"

					switch rec.status {
					case http.StatusUnauthorized:
						outcome = "denied"
						reason = "unauthenticated"
					case http.StatusForbidden:
						outcome = "denied"
						reason = "forbidden"
					}

					authAttempts.Add(r.Context(), 1, metric.WithAttributes(
						attribute.String("outcome", outcome),
						attribute.String("reason", reason),
						attribute.String("http.route", route),
					))
				}
			}

			// Slow request span event for P99 triage
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				span.SetAttributes(attribute.String("http.route", route))

				if elapsed > p99Budget {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.String("http.route", route),
						attribute.Float64("duration_s", elapsed.Seconds()),
						attribute.Float64("budget_s", p99Budget.Seconds()),
						attribute.Int("http.response.status_code", rec.status),
					))
				}

				if rec.status >= 500 {
					span.SetAttributes(attribute.String("error.type", strconv.Itoa(rec.status)))
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

func protoVersion(proto string) string {
	switch proto {
	case "HTTP/1.0":
		return "1.0"
	case "HTTP/1.1":
		return "1.1"
	case "HTTP/2.0":
		return "2"
	default:
		return proto
	}
}
