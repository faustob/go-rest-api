// ----------------------------------------------------------------------------
// Auth telemetry middleware — wraps the existing JWT auth check and records
// auth.attempts with outcome and denial_reason attributes.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// authTelemetryMiddleware records auth.attempts for every request that passes
// through the JWT authentication layer. It must be placed AFTER the JWT
// middleware so it can inspect whether the request was rejected.
//
// Usage: wrap the authenticated sub-router, not the public health endpoints.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		if globalMetrics == nil {
			return
		}

		outcome := "success"
		denialReason := ""
		if rw.status == http.StatusUnauthorized {
			outcome = "denied"
			denialReason = "unauthorized"
		} else if rw.status == http.StatusForbidden {
			outcome = "denied"
			denialReason = "forbidden"
		}

		attrs := []metric.AddOption{
			metric.WithAttributes(
				attribute.String("outcome", outcome),
				attribute.String("denial_reason", denialReason),
			),
		}
		globalMetrics.authAttempts.Add(r.Context(), 1, attrs...)

		// flow.validation.outcomes — record the auth step result.
		validationOutcome := "passed"
		if outcome == "denied" {
			validationOutcome = "failed"
		}
		globalMetrics.flowValidationOutcomes.Add(r.Context(), 1,
			metric.WithAttributes(
				attribute.String("step", "jwt_auth"),
				attribute.String("outcome", validationOutcome),
			),
		)
	})
}
