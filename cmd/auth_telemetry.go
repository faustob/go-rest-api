// ----------------------------------------------------------------------------
// Auth telemetry middleware — emits auth.attempts counter tagged with outcome
// and reason, supporting the Authentication Failure Rate SLI.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	authMeter    = otel.Meter("go-rest-api/auth")
	authAttempts metric.Int64Counter
)

func init() {
	var err error
	authAttempts, err = authMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
	)
	if err != nil {
		panic(err)
	}
}

// authTelemetryMiddleware wraps an http.Handler and records auth outcomes.
// It inspects the response status: 401 → unauthenticated, 403 → forbidden,
// everything else → success.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		outcome := "success"
		reason := ""
		switch rw.status {
		case http.StatusUnauthorized:
			outcome = "denied"
			reason = "unauthenticated"
		case http.StatusForbidden:
			outcome = "denied"
			reason = "forbidden"
		}

		attrs := []attribute.KeyValue{
			attribute.String("outcome", outcome),
		}
		if reason != "" {
			attrs = append(attrs, attribute.String("denial.reason", reason))
		}
		authAttempts.Add(r.Context(), 1, metric.WithAttributes(attrs...))
	})
}

// statusRecorder captures the HTTP status code written by the inner handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
