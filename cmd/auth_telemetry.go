// ----------------------------------------------------------------------------
// Auth telemetry middleware — wraps the JWT validator middleware to record
// auth.attempts with outcome and denial reason (auth failure rate SLI).
// ----------------------------------------------------------------------------

package main

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
)

// authTelemetryMiddleware wraps an inner http.Handler (typically the JWT
// validator middleware chain) and records auth.attempts for every request
// that carries an Authorization header, tagging the outcome.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if globalMetrics == nil || r.Header.Get("Authorization") == "" {
			// No auth header — not an auth attempt; pass through.
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()

		// Capture the response status to determine auth outcome.
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sr, r)

		outcome := "allowed"
		reason := ""
		if sr.status == http.StatusUnauthorized {
			outcome = "denied"
			reason = "unauthorized"
		} else if sr.status == http.StatusForbidden {
			outcome = "denied"
			reason = "forbidden"
		}

		attrs := []attribute.KeyValue{
			attribute.String("outcome", outcome),
		}
		if reason != "" {
			attrs = append(attrs, attribute.String("denial.reason", reason))
		}

		globalMetrics.authAttempts.Add(ctx, 1, attrs...)
	})
}
