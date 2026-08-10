// ----------------------------------------------------------------------------
// chi middleware emitting request outcome / throughput / auth outcome metrics.
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// statusRecorder captures the response status code while preserving the full
// http.ResponseWriter contract (Flush / Hijack / ReadFrom are forwarded).
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

	return nil, nil, errors.New("telemetry: underlying ResponseWriter does not implement http.Hijacker")
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

func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// RequestMetricsMiddleware records request outcome counts (availability, error
// rate, throughput) and marks slow requests with a span event for P99 triage.
// Latency itself comes from otelhttp's http.server.request.duration histogram.
func RequestMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()

		inFlightAttrs := metric.WithAttributes(attribute.String("http.request.method", r.Method))
		if requestsInFlight != nil {
			requestsInFlight.Add(ctx, 1, inFlightAttrs)
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if requestsInFlight != nil {
				requestsInFlight.Add(ctx, -1, inFlightAttrs)
			}

			elapsed := time.Since(start)

			// Route pattern is only populated AFTER routing has happened.
			route := "unmatched"
			if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
				route = rctx.RoutePattern()
			}

			class := statusClass(rec.status)

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("http.response.status_class", class),
				attribute.String("url.scheme", scheme(r)),
			}

			if rec.status >= 500 {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rec.status)))
			}

			if requestOutcomes != nil {
				requestOutcomes.Add(ctx, 1, metric.WithAttributes(attrs...))
			}

			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				span.SetAttributes(
					attribute.String("http.route", route),
					attribute.String("http.response.status_class", class),
				)

				if rec.status >= 500 {
					span.SetAttributes(attribute.String("error.type", strconv.Itoa(rec.status)))
				}

				if elapsed > slowRequestBudget {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.String("http.route", route),
						attribute.Float64("duration_s", elapsed.Seconds()),
						attribute.Float64("budget_s", slowRequestBudget.Seconds()),
					))
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// AuthOutcomeMiddleware counts every authentication decision. It must be
// registered BEFORE the JWT validator so denials (which short-circuit) are seen.
func AuthOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if authAttempts == nil {
				return
			}

			outcome := "allowed"
			reason := "none"

			switch rec.status {
			case http.StatusUnauthorized:
				outcome, reason = "denied", "unauthorized"
			case http.StatusForbidden:
				outcome, reason = "denied", "forbidden"
			}

			authAttempts.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("outcome", outcome),
				attribute.String("reason", reason),
				attribute.String("http.request.method", r.Method),
			))
		}()

		next.ServeHTTP(rec, r)
	})
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	return "http"
}
