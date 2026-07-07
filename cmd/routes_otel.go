// ----------------------------------------------------------------------------
// OTel instrumentation helpers for routes — slow-request span events.
// This file is part of the same `main` package so it can share instruments.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// instrumentedHandler wraps a chi handler to record flow-level telemetry:
// - flow.entries counter (throughput SLO)
// - flow.outcomes counter (availability / success-rate SLO)
// - flow.duration histogram (latency / freshness SLO)
// - flow.validation.outcomes counter (validation-failure-rate SLO)
// - http.server.active_requests gauge (saturation SLO — via activeRequests int64)
// - slow-request span event when elapsed > P99 budget (P99 SLO)
//
// NOTE: http.server.request.duration (availability, latency, error-rate, throughput SLOs)
// is emitted automatically by the otelhttp.NewMiddleware registered in server.go.
func instrumentedHandler(
	routePath string,
	next http.HandlerFunc,
) http.HandlerFunc {
	tracer := otel.Tracer("go-rest-api/routes")

	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Flow entry — throughput SLO
		flowEntries.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", routePath),
		))

		// Saturation SLO — track in-flight requests
		activeRequests++
		defer func() { activeRequests-- }()

		// Root flow span
		ctx, span := tracer.Start(ctx, "flow."+routePath,
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		// Validation step span
		_, valSpan := tracer.Start(ctx, "flow.validation."+routePath)
		valSpan.SetAttributes(
			attribute.String("http.route", routePath),
			attribute.String("validation.outcome", "passed"),
		)
		validationOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", routePath),
			attribute.String("outcome", "passed"),
		))
		valSpan.End()

		start := time.Now()
		next(w, r.WithContext(ctx))
		elapsed := time.Since(start).Seconds()

		// Slow-request span event — P99 SLO triage
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.Float64("elapsed_s", elapsed),
				attribute.String("http.route", routePath),
			))
		}

		// Flow duration — freshness / P95 SLO
		flowDuration.Record(ctx, elapsed, metric.WithAttributes(
			attribute.String("http.route", routePath),
			attribute.String("outcome", "success"),
		))

		// Flow outcome — E2E success-rate SLO
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", routePath),
			attribute.String("outcome", "success"),
		))
	}
}

// addPublicRoutesOTel registers the public routes wrapped with OTel instrumentation.
// Called from addPublicRoutes in the existing routes wiring.
func (api ThingAPI) addPublicRoutesOTel(r chi.Router) {
	r.Get("/things", instrumentedHandler("/things", api.getThings))
	r.Get("/things/{id}", instrumentedHandler("/things/{id}", api.getThingByID))
}

// addProtectedRoutesOTel registers the protected routes wrapped with OTel instrumentation.
func (api ThingAPI) addProtectedRoutesOTel(r chi.Router) {
	r.Post("/things", instrumentedHandler("/things", api.createThing))
	r.Delete("/things/{id}", instrumentedHandler("/things/{id}", api.deleteThing))
}

// authAttemptCounter counts JWT auth attempts for the auth-failure-rate SLO.
var authAttemptCounter, _ = func() (metric.Int64Counter, error) {
	return otel.Meter("go-rest-api/auth").Int64Counter(
		"auth.attempts",
		metric.WithDescription("Number of JWT authentication attempts"),
		metric.WithUnit("{attempt}"),
	)
}()

// RecordAuthOutcome records an auth attempt outcome.
// Call this from the JWT middleware: RecordAuthOutcome(ctx, "allowed") or RecordAuthOutcome(ctx, "denied").
func RecordAuthOutcome(ctx context.Context, outcome string) {
	authAttemptCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
	))
}
