// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Auth telemetry middleware — records auth.attempts counter with outcome and
// denial reason so the Authentication Failure Rate SLI is computable.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/metric"
)

// authTelemetryMiddleware wraps the router to count every inbound request as
// an auth attempt.  It inspects the Authorization header:
//   - present  → outcome="success"
//   - absent   → outcome="denied", reason="missing_credentials"
//
// When JWKS_URI is not set the service runs without auth; in that case every
// request is counted as outcome="success" so the SLI denominator is correct.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outcome := "success"
		reason := ""

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			if jwksURIConfigured() {
				outcome = "denied"
				reason = "missing_credentials"
			}
		}

		attrs := []metric.MeasurementOption{
			metricAttr("outcome", outcome),
		}
		if reason != "" {
			attrs = append(attrs, metricAttr("denial.reason", reason))
		}
		authAttemptsCounter.Add(r.Context(), 1, attrs...)

		next.ServeHTTP(w, r)
	})
}

// jwksURIConfigured returns true when JWT auth is enabled.
func jwksURIConfigured() bool {
	import_os_getenv := func(k string) string {
		import "os"
		return os.Getenv(k)
	}
	return import_os_getenv("JWKS_URI") != ""
}
