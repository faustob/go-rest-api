// ----------------------------------------------------------------------------
// HTTP middleware recording request-outcome, latency, saturation and
// business-flow telemetry. Wired into the chi router in cmd/server.go.
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-chi/chi/v5"
)

// statusRecorder wraps http.ResponseWriter to capture the status code while
// forwarding all optional interfaces the underlying writer may implement.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying ResponseWriter's http.Flusher, if implemented,
// so streaming/SSE responses are not broken by this wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter's http.Hijacker, if
// implemented, so connection hijacking (e.g. websocket upgrades) still works.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// ReadFrom forwards to the underlying ResponseWriter's io.ReaderFrom, if
// implemented, preserving efficient copies (e.g. sendfile).
func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// P99 latency budget used to flag slow-request span events (750ms).
const p99BudgetSeconds = 0.750

// RequestTelemetryMiddleware records the http.server.request.outcomes counter,
// flow entry/outcome counters+span, flow duration histogram, and slow-request
// span events — without altering control flow or response codes.
func RequestTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequests, 1)
		defer atomic.AddInt64(&activeRequests, -1)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}

		if flowEntryCounter != nil {
			flowEntryCounter.Add(r.Context(), 1)
		}

		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unknown"
		}

		outcome := "success"
		if rec.status >= 500 {
			outcome = "server_error"
		} else if rec.status >= 400 {
			outcome = "client_error"
		}

		if requestOutcomeCounter != nil {
			requestOutcomeCounter.Add(r.Context(), 1,
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("outcome", outcome),
			)
		}

		if flowOutcomeCounter != nil {
			flowOutcome := "success"
			if rec.status >= 400 {
				flowOutcome = "failed"
			}
			flowOutcomeCounter.Add(r.Context(), 1, attribute.String("outcome", flowOutcome))
		}

		if flowDurationHist != nil {
			flowDurationHist.Record(r.Context(), duration, attribute.String("http.route", route))
		}

		// Slow-request span event when P99 budget is exceeded, only if a real span exists
		if duration > p99BudgetSeconds {
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				span.AddEvent("slow_request_budget_exceeded", trace.WithAttributes(
					attribute.Float64("http.server.request.duration", duration),
					attribute.String("http.route", route),
				))
			}
			if rec.status >= 500 {
				span.SetAttributes(attribute.String("error.type", "server_error"))
			}
		}
		}
	})
}

// AuthTelemetryMiddleware records the auth.attempts outcome counter around the
// existing JWT validator middleware, tagged with a low-cardinality reason.
func AuthTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: 200}

		next.ServeHTTP(rec, r)

		if authAttemptsCounter == nil {
			return
		}

		outcome := "allowed"
		reason := "none"
		if rec.status == http.StatusUnauthorized {
			outcome = "denied"
			reason = "unauthorized"
		} else if rec.status == http.StatusForbidden {
			outcome = "denied"
			reason = "forbidden"
		}

		authAttemptsCounter.Add(r.Context(), 1,
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		)
	})
}
