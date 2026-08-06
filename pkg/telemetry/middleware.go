// ----------------------------------------------------------------------------
// HTTP telemetry middleware: records semconv server duration, request outcome,
// in-flight requests, auth decision outcomes and slow-request span events.
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99Budget is the latency budget above which a span event is emitted for triage.
const p99Budget = 750 * time.Millisecond

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

	return nil, nil, http.ErrNotSupported
}

func (s *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
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
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// HTTPTelemetryMiddleware records HTTP server SLI metrics for every request.
// Register it AFTER middleware.Recoverer and BEFORE any auth middleware so that
// auth denials (401/403 short-circuits) are still observed.
func HTTPTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		inflightAttrs := metric.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", scheme),
		)

		if requestsActive != nil {
			requestsActive.Add(ctx, 1, inflightAttrs)
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// finally-style recording: always runs, never swallows a panic
		defer func() {
			elapsed := time.Since(start)

			if requestsActive != nil {
				requestsActive.Add(ctx, -1, inflightAttrs)
			}

			// Route pattern is only populated AFTER routing has happened
			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}

			if route == "" {
				route = "unmatched"
			}

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("url.scheme", scheme),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("network.protocol.version", protocolVersion(r.Proto)),
				attribute.String("tenant.tier", tenantTier(r)),
			}

			if rec.status >= 500 {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rec.status)))
			}

			if serverDuration != nil {
				serverDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))
			}

			if requestsTotal != nil {
				requestsTotal.Add(ctx, 1, metric.WithAttributes(append(attrs,
					attribute.String("http.response.status_class", outcomeClass(rec.status)),
				)...))
			}

			// Auth decision outcome for the auth failure rate SLI
			if authAttempts != nil && hasCredentials(r) {
				outcome := "allowed"
				reason := "none"

				switch rec.status {
				case http.StatusUnauthorized:
					outcome = "denied"
					reason = "unauthorized"
				case http.StatusForbidden:
					outcome = "denied"
					reason = "forbidden"
				}

				authAttempts.Add(ctx, 1, metric.WithAttributes(
					attribute.String("outcome", outcome),
					attribute.String("reason", reason),
					attribute.String("http.route", route),
				))
			}

			// Enrich the server span: status class, error type and slow-request event
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				span.SetAttributes(
					attribute.String("http.response.status_class", outcomeClass(rec.status)),
					attribute.String("tenant.tier", tenantTier(r)),
				)

				if rec.status >= 500 {
					span.SetAttributes(attribute.String("error.type", strconv.Itoa(rec.status)))
					span.SetStatus(codes.Error, outcomeClass(rec.status))
				}

				if elapsed > p99Budget {
					span.AddEvent("http.slow_request", trace.WithAttributes(
						attribute.Float64("duration_s", elapsed.Seconds()),
						attribute.Float64("budget_s", p99Budget.Seconds()),
						attribute.String("http.route", route),
					))
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

func protocolVersion(proto string) string {
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

// hasCredentials reports whether the request carried an auth credential, so we
// only count actual authentication/authorization decisions.
func hasCredentials(r *http.Request) bool {
	if r.Header.Get("Authorization") != "" {
		return true
	}

	return r.Header.Get("X-Api-Key") != ""
}

// tenantTier returns a LOW-cardinality business dimension for cohort-aware SLOs.
func tenantTier(r *http.Request) string {
	tier := r.Header.Get("X-Tenant-Tier")

	switch tier {
	case "free", "standard", "premium", "internal":
		return tier
	default:
		return "unknown"
	}
}
