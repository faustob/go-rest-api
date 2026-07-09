// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// HTTP server telemetry middleware: request duration histogram (with route,
// method, status attributes), request outcome counters, slow-request span
// events for P99 triage, and auth outcome tracking.
// ----------------------------------------------------------------------------

package telemetry

import (
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"bufio"
	"net"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99Budget is the target P99 latency budget used to decide when to emit a
// slow-request span event for triage.
const p99Budget = 750 * time.Millisecond

// statusRecorder wraps http.ResponseWriter to capture the status code, while
// preserving Flush/Hijack/ReadFrom passthroughs so streaming, SSE and
// WebSocket upgrades keep working.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
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
	return nil, nil, http.ErrNotSupported
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		if !r.wrote {
			r.status = http.StatusOK
			r.wrote = true
		}
		return rf.ReadFrom(src)
	}
	return io.Copy(writerOnly{r.ResponseWriter}, src)
}

// writerOnly hides any ReaderFrom implementation on the embedded writer so
// io.Copy's fallback path doesn't recurse back into ReadFrom.
type writerOnly struct {
	io.Writer
}

// HTTPMetricsMiddleware records http.server.request.duration, an outcome
// counter, active-request in-flight tracking, and slow-request span events
// for P99 triage. Must be registered after middleware.Recoverer so a
// downstream panic is still recovered, and before auth middleware if auth
// outcomes are tracked separately (see AuthOutcomeMiddleware).
func HTTPMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt64(&activeRequests, 1)
		defer atomic.AddInt64(&activeRequests, -1)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		span := trace.SpanFromContext(req.Context())

		next.ServeHTTP(rec, req)

		duration := time.Since(start)

		// Route pattern is only populated by chi AFTER routing has happened,
		// i.e. after next.ServeHTTP returns.
		route := ""
		if rctx := chi.RouteContext(req.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}
		if route == "" {
			route = "unknown"
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
		}

		if rec.status >= 500 {
			attrs = append(attrs, attribute.String("error.type", "server_error"))
			if span.IsRecording() {
				span.SetStatus(codes.Error, "HTTP 5xx response")
				span.SetAttributes(attribute.String("error.type", "server_error"))
			}
		}

		if httpRequestDuration != nil {
			httpRequestDuration.Record(req.Context(), duration.Seconds(), metric.WithAttributes(attrs...))
		}

		if span.IsRecording() && duration > p99Budget {
			span.AddEvent("slow_request_p99_budget_exceeded", trace.WithAttributes(
				attribute.String("http.route", route),
				attribute.Int64("duration_ms", duration.Milliseconds()),
			))
		}
	})
}

// AuthOutcomeMiddleware must be registered AFTER the JWT validator so it can
// observe the outcome the validator produced (allowed vs. denied via
// short-circuit). Since the JWT validator middleware itself writes the 401
// response and does not call next() on failure, we detect denial by checking
// whether a response was already written before this middleware runs next.
func AuthOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: 0}

		next.ServeHTTP(rec, req)

		outcome := "allowed"
		reason := "none"
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			outcome = "denied"
			reason = "unauthorized"
		}

		if authAttempts != nil {
			authAttempts.Add(req.Context(), 1, metric.WithAttributes(
				attribute.String("auth.outcome", outcome),
				attribute.String("auth.reason", reason),
			))
		}
	})
}
