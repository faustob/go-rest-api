// ----------------------------------------------------------------------------
// Auth telemetry middleware — wraps the JWT validator middleware to emit
// auth.attempts counter with outcome and denial reason attributes.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"io"
	"net"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	authMeter          = otel.Meter("go-rest-api/auth")
	authOutcomeCounter metric.Int64Counter
)

func init() {
	var err error
	authOutcomeCounter, err = authMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions tagged by outcome and denial reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		panic(err)
	}
}

// authTelemetryMiddleware wraps an existing JWT-protected handler chain and
// records auth.attempts with outcome="allowed" or outcome="denied".
// It inspects the response status: 401/403 → denied, otherwise → allowed.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		outcome := "allowed"
		denialReason := ""
		if rw.status == http.StatusUnauthorized {
			outcome = "denied"
			denialReason = "unauthorized"
		} else if rw.status == http.StatusForbidden {
			outcome = "denied"
			denialReason = "forbidden"
		}
		attrs := []attribute.KeyValue{
			attribute.String("outcome", outcome),
		}
		if denialReason != "" {
			attrs = append(attrs, attribute.String("denial.reason", denialReason))
		}
		authOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	})
}

// statusRecorder captures the HTTP status code written by the downstream handler.
// It forwards the optional http.Flusher, http.Hijacker, and io.ReaderFrom interfaces
// so that streaming, WebSocket upgrades, and sendfile continue to work.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := sr.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (sr *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := sr.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(sr.ResponseWriter, r)
}
