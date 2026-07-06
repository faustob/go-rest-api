// ----------------------------------------------------------------------------
// Auth telemetry middleware — wraps the JWT validator middleware to record
// auth.attempts counters for every authentication decision.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
)

// authTelemetryMiddleware wraps an existing http.Handler (typically the JWT
// validator middleware chain) and records auth attempt outcomes.
// It detects a 401/403 response as a denial and records the status class as
// the denial reason (low-cardinality).
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sr := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(sr, r)

		switch {
		case sr.statusCode == http.StatusUnauthorized:
			RecordAuthAttempt(r.Context(), "denied", "unauthorized")
		case sr.statusCode == http.StatusForbidden:
			RecordAuthAttempt(r.Context(), "denied", "forbidden")
		default:
			RecordAuthAttempt(r.Context(), "allowed", "")
		}
	})
}
