// ----------------------------------------------------------------------------
// Auth telemetry middleware — records auth.attempts counter with outcome label
// so the Authentication Failure Rate SLI is computable.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// authTelemetryMeter is the meter for auth-specific metrics.
var authTelemetryMeter = otel.Meter("go-rest-api/auth")

// authAttemptsCounter counts every JWT authentication decision.
// outcome: "allowed" | "denied"
// denial_reason: "invalid_token" | "missing_token" | "insufficient_scope" | "unknown"
var authAttemptsCounter, _ = authTelemetryMeter.Int64Counter(
	"auth.attempts",
	metric.WithDescription("Total JWT authentication/authorization decisions"),
	metric.WithUnit("{attempt}"),
)

// authTelemetryMiddleware wraps an existing http.Handler and records auth
// outcomes by inspecting the response status code written by the upstream
// JWT validator middleware. A 401 or 403 is counted as "denied"; anything
// else is counted as "allowed".
//
// Wire this AFTER the JWT validator middleware so the status code is already
// set when we observe it.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &authStatusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		outcome := "allowed"
		denialReason := ""
		if rw.status == http.StatusUnauthorized {
			outcome = "denied"
			denialReason = "invalid_token"
		} else if rw.status == http.StatusForbidden {
			outcome = "denied"
			denialReason = "insufficient_scope"
		}

		attrs := []attribute.KeyValue{
			attribute.String("outcome", outcome),
		}
		if denialReason != "" {
			attrs = append(attrs, attribute.String("denial_reason", denialReason))
		}
		authAttemptsCounter.Add(r.Context(), 1, metric.WithAttributes(attrs...))
	})
}

// authStatusRecorder is a minimal http.ResponseWriter wrapper that captures
// the HTTP status code so we can classify auth outcomes.
type authStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *authStatusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
