// ----------------------------------------------------------------------------
// chi-compatible OpenTelemetry middleware:
//   - HTTPMetricsMiddleware: semconv http.server.request.duration + outcome /
//     throughput counters + slow-request span events.
//   - AuthOutcomeMiddleware: auth.attempts counter (granted / denied).
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

// p99LatencyBudget is the P99 SLO target; exceeding it adds a span event for triage.
const p99LatencyBudget = 750 * time.Millisecond

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

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
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

// tenantOf returns a LOW cardinality business dimension for cohort-aware SLOs.
func tenantOf(req *http.Request) string {
	if tier := req.Header.Get("X-Tenant-Tier"); tier != "" {
		return tier
	}

	return "unknown"
}

// HTTPMetricsMiddleware records semantic-convention HTTP server metrics.
func HTTPMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		tier := tenantOf(req)

		if requestsInFlight != nil {
			requestsInFlight.Add(ctx, 1, metric.WithAttributes(
				attribute.String("http.request.method", req.Method),
			))
		}

		recorder := &statusRecorder{ResponseWriter: resp, status: http.StatusOK}
		start := time.Now()

		defer func() {
			elapsed := time.Since(start)

			if requestsInFlight != nil {
				requestsInFlight.Add(ctx, -1, metric.WithAttributes(
					attribute.String("http.request.method", req.Method),
				))
			}

			// Route pattern is only populated AFTER routing has happened
			route := ""
			if rctx := chi.RouteContext(req.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}

			if route == "" {
				route = "unmatched"
			}

			scheme := "http"
			if req.TLS != nil {
				scheme = "https"
			}

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", req.Method),
				attribute.String("url.scheme", scheme),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", recorder.status),
				attribute.String("network.protocol.version", req.Proto),
				attribute.String("tenant.tier", tier),
			}

			if recorder.status >= 400 {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(recorder.status)))
			}

			if requestDuration != nil {
				requestDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))
			}

			if requestOutcome != nil {
				requestOutcome.Add(ctx, 1, metric.WithAttributes(
					attribute.String("http.request.method", req.Method),
					attribute.String("http.route", route),
					attribute.Int("http.response.status_code", recorder.status),
					attribute.String("http.outcome", outcomeClass(recorder.status)),
					attribute.String("tenant.tier", tier),
				))
			}

			span := trace.SpanFromContext(req.Context())
			if span.IsRecording() {
				span.SetAttributes(
					attribute.String("http.route", route),
					attribute.String("tenant.tier", tier),
				)

				if recorder.status >= 500 {
					span.SetAttributes(attribute.String("error.type", strconv.Itoa(recorder.status)))
				}

				if elapsed > p99LatencyBudget {
					span.AddEvent("slow.request", trace.WithAttributes(
						attribute.Float64("duration.s", elapsed.Seconds()),
						attribute.Float64("budget.s", p99LatencyBudget.Seconds()),
						attribute.String("http.route", route),
					))
				}
			}
		}()

		next.ServeHTTP(recorder, req)
	})
}

// AuthOutcomeMiddleware counts authentication/authorization decisions. It is
// registered immediately BEFORE the JWT validator so short-circuited (denied)
// requests are observed too.
func AuthOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		recorder := &statusRecorder{ResponseWriter: resp, status: http.StatusOK}

		defer func() {
			if authAttempts == nil {
				return
			}

			outcome := "granted"
			reason := "none"

			switch recorder.status {
			case http.StatusUnauthorized:
				outcome, reason = "denied", "unauthenticated"
			case http.StatusForbidden:
				outcome, reason = "denied", "insufficient_scope"
			}

			route := ""
			if rctx := chi.RouteContext(req.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}

			if route == "" {
				route = "unmatched"
			}

			authAttempts.Add(req.Context(), 1, metric.WithAttributes(
				attribute.String("auth.outcome", outcome),
				attribute.String("auth.denied.reason", reason),
				attribute.String("http.route", route),
				attribute.String("http.request.method", req.Method),
			))
		}()

		next.ServeHTTP(recorder, req)
	})
}
