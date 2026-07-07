// ----------------------------------------------------------------------------
// Auth telemetry middleware — wraps the JWT validator middleware to emit
// auth attempt outcome counters for the Authentication Failure Rate SLI.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	authMeter          = otel.Meter("go-rest-api/auth")
	authAttemptCounter metric.Int64Counter
)

func init() {
	var err error
	authAttemptCounter, err = authMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total authentication attempts by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		panic(err)
	}
}

// authTelemetryMiddleware wraps an existing HTTP handler and records auth
// attempt outcomes. It must be placed AFTER the JWT validator middleware so
// that a 401 response written by the validator is visible here.
//
// Usage: wrap the protected-router group handler at the chi wiring site.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		outcome := "success"
		reason := ""
		if rw.status == http.StatusUnauthorized {
			outcome = "denied"
			reason = "unauthorized"
		} else if rw.status == http.StatusForbidden {
			outcome = "denied"
			reason = "forbidden"
		}
		attrs := []attribute.KeyValue{
			attribute.String("outcome", outcome),
		}
		if reason != "" {
			attrs = append(attrs, attribute.String("denial.reason", reason))
		}
		authAttemptCounter.Add(r.Context(), 1, metric.WithAttributes(attrs...))
	})
}

// statusRecorder captures the HTTP status code written by downstream handlers.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
