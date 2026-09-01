// ----------------------------------------------------------------------------
// Custom auth telemetry middleware: emits a counter for every
// authentication/authorization decision, tagged with the outcome and, on
// denial, the reason - without altering the JWT validator's behavior.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"

	"github.com/benc-uk/go-rest-api/pkg/telemetry"
	"github.com/go-chi/chi/middleware"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// authMetricsMiddleware MUST be registered BEFORE the JWT validator
// middleware (protectedRouter.Use(authMetricsMiddleware) then
// protectedRouter.Use(jwtValidator.Middleware)) so it wraps the validator and
// can observe the response status it produces when it denies a request.
func authMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		status := ww.Status()

		outcome := "granted"
		reason := "none"
		switch status {
		case http.StatusUnauthorized:
			outcome = "denied"
			reason = "unauthorized"
		case http.StatusForbidden:
			outcome = "denied"
			reason = "forbidden"
		}

		telemetry.AuthAttemptsCounter.Add(r.Context(), 1, otelmetric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}
