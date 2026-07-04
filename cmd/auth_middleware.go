// auth_middleware.go — OTel instrumentation wrapper around the JWT validator middleware.
// It records auth.attempts counters (outcome: success|denied) so the
// Authentication Failure Rate SLI is computable.
package main

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// authInstrumentedMiddleware wraps an existing http.Handler (the JWT validator middleware
// chain) and records auth attempt outcomes.
// Usage: protectedRouter.Use(authInstrumentedMiddleware(jwtValidator.Middleware))
func authInstrumentedMiddleware(next func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		inner := next(h)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture the response status to determine auth outcome.
			rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			inner.ServeHTTP(rw, r)
			if rw.status == http.StatusUnauthorized || rw.status == http.StatusForbidden {
				authAttempts.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("outcome", "denied"),
					attribute.String("reason", http.StatusText(rw.status)),
				))
			} else {
				authAttempts.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("outcome", "success"),
					attribute.String("reason", ""),
				))
			}
		})
	}
}

// statusRecorder is a minimal http.ResponseWriter wrapper that captures the status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
