// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation for the go-rest-api service.
//
// This package owns the SINGLE meter/tracer scope for the service and every
// instrument created from it. It is a library: it never builds or registers an
// SDK -- it only uses the globally registered providers (see cmd/otel.go, which
// the application's main() invokes at startup).
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope for every signal this package emits.
const ScopeName = "github.com/benc-uk/go-rest-api"

// p99Budget is the P99 latency objective. Requests slower than this get a span
// event so the tail can be triaged from the trace.
const p99Budget = 750 * time.Millisecond

// ONE meter and ONE tracer for the whole service; every instrument below is
// created from this meter.
var (
	meter  = otel.Meter(ScopeName)
	tracer = otel.Tracer(ScopeName)
)

var (
	// requestDuration is the OTel semantic-convention inbound request duration
	// histogram, in SECONDS. Backs the P95/P99 latency SLIs, and its _count
	// series backs availability, 5xx error rate and throughput.
	requestDuration metric.Float64Histogram

	// requestOutcome counts requests by route and outcome class, so availability
	// and throughput are computable without scanning traces.
	requestOutcome metric.Int64Counter

	// authAttempts counts every authentication/authorization decision.
	authAttempts metric.Int64Counter
)

// instrument construction errors are captured so a failure is visible without
// panicking the application at import time.
var initErrs []error

func init() {
	var err error

	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests."),
	)
	if err != nil {
		initErrs = append(initErrs, err)
	}

	requestOutcome, err = meter.Int64Counter(
		"http.server.request.outcome",
		metric.WithDescription("Inbound HTTP requests by route and outcome class."),
	)
	if err != nil {
		initErrs = append(initErrs, err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication and authorization decisions by outcome."),
	)
	if err != nil {
		initErrs = append(initErrs, err)
	}
}

// InitErrors returns any errors hit while creating the instruments.
func InitErrors() []error {
	return initErrs
}

// statusRecorder wraps http.ResponseWriter purely to capture the status code.
// It forwards every optional interface the inner writer may implement, so SSE,
// streaming, WebSocket upgrades and the io.Copy fast path keep working.
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

func (s *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := s.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}

	return http.ErrNotSupported
}

func (s *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}

	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}

	return io.Copy(struct{ io.Writer }{s.ResponseWriter}, r)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// outcomeClass maps a status code to a low-cardinality outcome class.
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

// tenantOf returns a LOW-CARDINALITY tenant/cohort dimension for the request.
// It deliberately never returns a raw API key, token or user id.
func tenantOf(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-Tier"); t != "" {
		switch strings.ToLower(t) {
		case "free", "standard", "premium", "enterprise":
			return strings.ToLower(t)
		}

		return "other"
	}

	if r.Header.Get("Authorization") != "" {
		return "authenticated"
	}

	return "anonymous"
}

// Middleware records semantic-convention HTTP server metrics for every request.
//
// It is a chi middleware, so it must be registered with router.Use(...). The
// matched route TEMPLATE is only populated by chi AFTER the request has been
// routed, so it is read once next.ServeHTTP returns -- never the raw URL path.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Defer the recording so telemetry is emitted even if a downstream
		// handler panics (Recoverer runs upstream and still handles the panic).
		defer func() {
			elapsed := time.Since(start)

			route := "other"
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					route = pattern
				}
			}

			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}

			tier := tenantOf(r)
			class := outcomeClass(rec.status)

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("url.scheme", scheme),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("network.protocol.version", strings.TrimPrefix(r.Proto, "HTTP/")),
				attribute.String("tenant.tier", tier),
			}

			// error.type carries the status CLASS, never a message.
			if rec.status >= 500 {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rec.status)))
			}

			// Duration in SECONDS, per semantic conventions.
			requestDuration.Record(r.Context(), elapsed.Seconds(), metric.WithAttributes(attrs...))

			requestOutcome.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("http.response.status_class", class),
				attribute.String("tenant.tier", tier),
			))

			// Tail-latency triage: annotate the active server span (created by
			// otelhttp) when the request blows the P99 budget, and attribute 5xx
			// responses to a status class on the span.
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				span.SetAttributes(
					attribute.String("http.route", route),
					attribute.Int("http.response.status_code", rec.status),
				)

				if rec.status >= 500 {
					span.SetAttributes(attribute.String("error.type", strconv.Itoa(rec.status)))
				}

				if elapsed > p99Budget {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.String("http.route", route),
						attribute.Float64("duration_s", elapsed.Seconds()),
						attribute.Float64("budget_s", p99Budget.Seconds()),
					))
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// StartSpan starts a child span on the service tracer and returns the derived
// context so downstream calls nest correctly under it.
func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return tracer.Start(ctx, name)
}

// DenialReason derives a LOW-CARDINALITY reason class for a denied auth
// attempt from the request shape. It never returns token contents.
func DenialReason(r *http.Request, jwksMissing bool) string {
	if jwksMissing {
		return "jwks_unavailable"
	}

	authHeader := r.Header.Get("Authorization")
	if len(authHeader) == 0 {
		return "missing_credentials"
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return "malformed_credentials"
	}

	return "token_rejected"
}

// RecordAuthAttempt records one authentication/authorization decision.
// reason is only used when allowed is false.
func RecordAuthAttempt(ctx context.Context, allowed bool, reason string) {
	outcome := "denied"
	if allowed {
		outcome = "allowed"
	}

	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}

	if !allowed && reason != "" {
		attrs = append(attrs,
			attribute.String("auth.denial.reason", reason),
			attribute.String("error.type", reason),
		)
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))
}
