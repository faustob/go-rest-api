// ----------------------------------------------------------------------------
// OpenTelemetry chi middleware: request outcome counter, latency histogram
// (semconv http.server.request.duration in SECONDS), slow-request span events
// and auth attempt outcome counter.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99Budget is the P99 latency budget, requests slower than this get a span event.
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

// RequestTelemetryMiddleware records request duration, outcome and throughput,
// labelled with the matched chi route TEMPLATE (read after routing has happened).
func RequestTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: resp, status: http.StatusOK}

		// Never swallow a panic: record in a deferred func, then let it propagate.
		defer func() {
			elapsed := time.Since(start)

			// Route pattern is only populated AFTER routing, so read it here.
			route := "unmatched"
			if rctx := chi.RouteContext(req.Context()); rctx != nil && rctx.RoutePattern() != "" {
				route = rctx.RoutePattern()
			}

			scheme := "http"
			if req.TLS != nil {
				scheme = "https"
			}

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", req.Method),
				attribute.String("url.scheme", scheme),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("network.protocol.version", req.Proto),
				tenantAttr(req.Header.Get("X-Tenant-Tier")),
			}

			if rec.status >= 500 {
				attrs = append(attrs, attribute.String("error.type", statusClass(rec.status)))
			}

			set := metric.WithAttributes(attrs...)

			if requestDuration != nil {
				requestDuration.Record(req.Context(), elapsed.Seconds(), set)
			}

			if requestOutcomeCounter != nil {
				requestOutcomeCounter.Add(req.Context(), 1, metric.WithAttributes(
					append(attrs, attribute.String("http.response.status_class", statusClass(rec.status)))...,
				))
			}

			// Slow-request span event + exception/status class attribution on the server span.
			span := trace.SpanFromContext(req.Context())
			if span.IsRecording() {
				span.SetAttributes(
					attribute.String("http.route", route),
					attribute.Int("http.response.status_code", rec.status),
				)

				if rec.status >= 500 {
					span.SetAttributes(attribute.String("error.type", statusClass(rec.status)))
					span.SetStatus(codes.Error, statusClass(rec.status))
				}

				if elapsed > p99Budget {
					span.AddEvent("slow.request", trace.WithAttributes(
						attribute.Float64("duration.s", elapsed.Seconds()),
						attribute.Float64("budget.s", p99Budget.Seconds()),
						attribute.String("http.route", route),
					))
				}
			}
		}()

		next.ServeHTTP(rec, req)
	})
}

// AuthTelemetryMiddleware counts authentication/authorization decisions. It is
// registered BEFORE the JWT validator so short-circuited denials are observed.
func AuthTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		rec := &statusRecorder{ResponseWriter: resp, status: http.StatusOK}

		ctx, span := tracer.Start(req.Context(), "auth.validate")
		defer span.End()

		req = req.WithContext(ctx)

		defer func() {
			outcome := "granted"
			reason := "none"

			switch {
			case rec.status == http.StatusUnauthorized:
				outcome, reason = "denied", "unauthenticated"
			case rec.status == http.StatusForbidden:
				outcome, reason = "denied", "insufficient_scope"
			case rec.status >= 500:
				outcome, reason = "error", statusClass(rec.status)
			}

			if authAttemptsCounter != nil {
				authAttemptsCounter.Add(ctx, 1, metric.WithAttributes(
					attribute.String("outcome", outcome),
					attribute.String("auth.denial.reason", reason),
					attribute.String("http.request.method", req.Method),
				))
			}

			span.SetAttributes(
				attribute.String("outcome", outcome),
				attribute.String("auth.denial.reason", reason),
			)

			if outcome != "granted" {
				span.SetStatus(codes.Error, outcome)
			}
		}()

		next.ServeHTTP(rec, req)
	})
}
