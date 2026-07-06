// ----------------------------------------------------------------------------
// Custom telemetry middleware — records per-request flow/auth/saturation SLIs
// that are not covered by otelhttp's automatic http.server.request.duration.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const p99BudgetSeconds = 0.750 // 750 ms P99 budget

// flowTelemetryMiddleware records:
//   - flow.entries (throughput SLI)
//   - flow.outcomes (success/failure SLI)
//   - flow.duration (latency / freshness SLI)
//   - flow.validation.outcomes (validation failure SLI)
//   - http.server.active_requests (saturation SLI)
//   - slow-request span event when handler exceeds P99 budget
func flowTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if globalMetrics == nil {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
		}

		// Flow entry counter (throughput SLI).
		globalMetrics.flowEntries.Add(ctx, 1, attrs...)

		// Validation outcome — treat presence of Authorization header as the
		// validation gate; absence is a validation failure for protected paths.
		hasAuth := r.Header.Get("Authorization") != ""
		validationOutcome := "pass"
		if !hasAuth {
			validationOutcome = "fail"
		}
		globalMetrics.flowValidationOutcomes.Add(ctx, 1,
			attribute.String("http.request.method", r.Method),
			attribute.String("outcome", validationOutcome),
		)

		// Track in-flight requests for saturation SLI.
		atomic.AddInt64(&globalMetrics.activeRequestsGauge, 1)
		defer atomic.AddInt64(&globalMetrics.activeRequestsGauge, -1)

		start := time.Now()

		// Delegate to the next handler.
		wrapped := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		duration := time.Since(start).Seconds()
		statusCode := wrapped.status

		// Determine flow outcome.
		outcome := "success"
		if statusCode >= 500 {
			outcome = "failure"
		}

		flowAttrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("outcome", outcome),
		}

		// flow.outcomes (success/failure SLI).
		globalMetrics.flowOutcomes.Add(ctx, 1, flowAttrs...)

		// flow.duration (latency / freshness SLI).
		globalMetrics.flowDuration.Record(ctx, duration, flowAttrs...)

		// Slow-request span event for P99 triage (latency P99 SLI).
		if duration > p99BudgetSeconds {
			span := trace.SpanFromContext(ctx)
			span.AddEvent("slow_request",
				trace.WithAttributes(
					attribute.Float64("handler.duration_s", duration),
					attribute.Int("http.response.status_code", statusCode),
					attribute.String("http.request.method", r.Method),
				),
			)
			if statusCode >= 500 {
				span.SetStatus(codes.Error, http.StatusText(statusCode))
			}
		}
	})
}

// statusRecorder wraps ResponseWriter to capture the status code while
// forwarding all optional interfaces (Flusher, Hijacker, ReadFrom) so that
// streaming, WebSocket, and SSE handlers continue to work correctly.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the inner ResponseWriter if it implements http.Flusher.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the inner ResponseWriter if it implements http.Hijacker.
func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := sr.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// ReadFrom forwards to the inner ResponseWriter if it implements io.ReaderFrom.
func (sr *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := sr.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	// Fallback: copy manually.
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			wn, werr := sr.ResponseWriter.Write(buf[:n])
			total += int64(wn)
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return total, err
		}
	}
	return total, nil
}
