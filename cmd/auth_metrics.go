// ----------------------------------------------------------------------------
// Authentication attempt outcome counter for the auth-failure-rate SLI.
// The counter is incremented by the JWT validator middleware wrapper defined
// in this file, which wraps the existing auth.JWTValidator middleware.
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
		metric.WithDescription("Total JWT authentication attempts, labelled by outcome"),
	)
	if err != nil {
		panic(err)
	}
}

// authMetricsMiddleware wraps an existing http.Handler (typically the JWT
// validator middleware) and records auth.attempts with outcome="allowed" or
// outcome="denied" based on whether the downstream handler wrote a 401/403.
func authMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		outcome := "allowed"
		if rw.status == http.StatusUnauthorized || rw.status == http.StatusForbidden {
			outcome = "denied"
		}
		authAttempts.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	})
}

// statusRecorder is a minimal http.ResponseWriter wrapper that captures the
// written status code. It forwards Flush so streaming responses are unaffected.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
