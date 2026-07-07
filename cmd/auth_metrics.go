// auth_metrics.go — Authentication attempt outcome counter for the auth-failure-rate SLI.
// The JWTValidator middleware is called for every protected route; we wrap it here
// to record auth.attempts with an outcome attribute.
package main

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	authMeter = otel.Meter("go-rest-api/auth")

	authAttemptsCounter metric.Int64Counter
)

func init() {
	var err error
	authAttemptsCounter, err = authMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Number of JWT authentication attempts, tagged by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		// Non-fatal: telemetry failure must not break the service
		_ = err
	}
}

// AuthMetricsMiddleware wraps an existing http.Handler and records auth attempt outcomes.
// Wire it around the JWT-protected group handler.
func AuthMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the response status by wrapping the ResponseWriter.
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
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
		if authAttemptsCounter != nil {
			authAttemptsCounter.Add(r.Context(), 1, metric.WithAttributes(attrs...))
		}
	})
}

// statusRecorder is a minimal ResponseWriter wrapper that captures the HTTP status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
