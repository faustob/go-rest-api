// ----------------------------------------------------------------------------
// Route-level telemetry helpers — wired into the existing route handlers to
// emit span events for slow requests (P99 budget) and validation outcome
// counters for the flow validation SLI.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	// p99BudgetSeconds is the P99 latency budget (750 ms). A span event is
	// added when a handler exceeds this threshold.
	p99BudgetSeconds = 0.750
)

// withRouteSpan starts a child span for the given route handler, records a
// slow-request span event when the handler exceeds the P99 budget, and sets
// error status on 5xx responses. It also records flow.validation.outcomes for
// the request validation step.
//
// Usage (inside a handler):
//
//	finish := withRouteSpan(resp, req, "/things/{id}", "getThingByID")
//	defer finish(statusPtr)
func withRouteSpan(
	w http.ResponseWriter,
	r *http.Request,
	routeTemplate string,
	operationName string,
) func(statusCode int) {
	tracer := otel.Tracer(meterScope)
	ctx, span := tracer.Start(r.Context(), operationName,
		trace.WithAttributes(
			attribute.String("http.route", routeTemplate),
			attribute.String("http.request.method", r.Method),
		),
	)
	_ = ctx // context is propagated via the span; child calls should use r.WithContext(ctx)
	start := time.Now()

	return func(statusCode int) {
		duration := time.Since(start).Seconds()

		span.SetAttributes(
			attribute.Int("http.response.status_code", statusCode),
		)

		if statusCode >= 500 {
			span.SetStatus(codes.Error, http.StatusText(statusCode))
			span.SetAttributes(attribute.String("error.type", http.StatusText(statusCode)))
		}

		if duration > p99BudgetSeconds {
			span.AddEvent("slow_request",
				trace.WithAttributes(
					attribute.Float64("handler.duration_s", duration),
					attribute.Float64("p99.budget_s", p99BudgetSeconds),
					attribute.String("http.route", routeTemplate),
				),
			)
		}

		// Record validation outcome for the flow validation SLI.
		validationOutcome := "passed"
		if statusCode >= 400 {
			validationOutcome = "failed"
		}
		RecordValidationOutcome(r.Context(), operationName, validationOutcome)

		span.End()
	}
}
