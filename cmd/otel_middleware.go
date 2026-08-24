// ----------------------------------------------------------------------------
// HTTP SLI instrumentation middleware for go-rest-api.
//
// Records the standard http.server.request.duration histogram plus the
// custom outcome/flow/tenant/saturation instruments defined in telemetry.go.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99Budget is the P99 response-time budget used to flag slow requests with a
// span event for triage.
const p99Budget = 750 * time.Millisecond

// statusRecorder wraps http.ResponseWriter to capture the status code
// actually written, while preserving the optional Flusher/Hijacker/ReaderFrom
// interfaces the underlying writer may implement (streaming, SSE, websocket
// upgrades, sendfile) so nothing downstream loses capability.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support http.Hijacker")
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// httpTelemetryMiddleware records the HTTP server SLI instruments: request
// duration (semconv http.server.request.duration), outcome counter,
// per-tenant throughput counter, in-flight gauge, and the primary-flow
// entry/outcome/duration instruments. Must be registered after
// middleware.Recoverer (so a downstream panic is still recovered) and after
// otelchi.Middleware (so a span already exists in the request context), and
// before any auth middleware whose denials it must count.
func httpTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		atomic.AddInt64(&activeRequests, 1)
		defer atomic.AddInt64(&activeRequests, -1)

		if flowEntries != nil {
			flowEntries.Add(r.Context(), 1)
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start)

		// Routing has happened by now, so the chi route pattern is populated.
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		outcome := "success"
		switch {
		case rec.status >= 500:
			outcome = "error"
		case rec.status >= 400:
			outcome = "client_error"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", scheme),
			attribute.Int("http.response.status_code", rec.status),
			attribute.String("http.route", route),
			attribute.String("network.protocol.version", strings.TrimPrefix(r.Proto, "HTTP/")),
		}
		if outcome == "error" {
			attrs = append(attrs, attribute.String("error.type", fmt.Sprintf("HTTP_%d", rec.status)))
		}

		if httpServerDuration != nil {
			httpServerDuration.Record(r.Context(), duration.Seconds(), metric.WithAttributes(attrs...))
		}

		tenant := r.Header.Get("X-Tenant-Id")
		if tenant == "" {
			tenant = "unknown"
		}
		if httpRequestOutcomes != nil {
			httpRequestOutcomes.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("http.route", route),
				attribute.String("outcome", outcome),
				attribute.String("tenant.id", tenant),
			))
		}

		// The whole inbound request is treated as the primary business flow's
		// entry-to-terminal span for this service.
		if flowOutcomes != nil {
			flowOutcomes.Add(r.Context(), 1, metric.WithAttributes(attribute.String("outcome", outcome)))
		}
		if flowDuration != nil {
			flowDuration.Record(r.Context(), duration.Seconds(), metric.WithAttributes(attribute.String("http.route", route)))
		}

		span := trace.SpanFromContext(r.Context())
		if outcome == "error" {
			span.SetAttributes(attribute.String("error.type", fmt.Sprintf("HTTP_%d", rec.status)))
		}
		if duration > p99Budget {
			span.AddEvent("slow.request", trace.WithAttributes(
				attribute.Float64("http.server.request.duration", duration.Seconds()),
				attribute.String("http.route", route),
			))
		}
	})
}

// authOutcomeMiddleware records the auth.attempts outcome counter. It must be
// registered BEFORE the JWT validator middleware so it also observes denied
// requests (the validator short-circuits the chain on rejection).
func authOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always wrap with a local statusRecorder rather than relying on an
		// upstream recorder being visible through intervening middleware
		// (e.g. SimpleCORSMiddleware may pass through a different writer).
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		outcome := "allowed"
		reason := "n/a"
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			outcome = "denied"
			reason = fmt.Sprintf("HTTP_%d", rec.status)
		}

		if authAttempts != nil {
			authAttempts.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("outcome", outcome),
				attribute.String("reason", reason),
			))
		}
	})
}
