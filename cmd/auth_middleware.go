// ----------------------------------------------------------------------------
// Auth telemetry middleware — wraps the JWT validator to emit auth.attempts
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"io"
	"net"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// authTelemetryMiddleware wraps a protected router's handler to record
// auth.attempts counters. It must be placed AFTER the JWT validator middleware
// so that a 401 response from the validator is visible here.
//
// Because chi's JWT middleware writes a 401 and calls return (not next.ServeHTTP),
// we capture the status code via a minimal response recorder and emit the counter.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		outcome := "success"
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			outcome = "denied"
		}
		authAttempts.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.Int("http.response.status_code", rec.status),
		))
	})
}

// statusRecorder is a minimal http.ResponseWriter wrapper that captures the
// written status code. It forwards Flush so streaming responses are unaffected.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
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
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}
