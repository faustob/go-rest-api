// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Some example routes for the thing API
// ----------------------------------------------------------------------------

package main

import (
	"errors"
	"net/http"
	"time"

	"github.com/benc-uk/go-rest-api/pkg/problem"
	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	otelTrace "go.opentelemetry.io/otel/trace"
)

// p99BudgetSeconds is the P99 latency SLO budget; a span event is added when exceeded.
const p99BudgetSeconds = 0.750

// routesTracer is the tracer used for per-handler spans in routes.go.
var routesTracer = otel.Tracer("go-rest-api/routes")

// routesMeter holds the OTel meter and business-metric instruments for routes.
var routesMeter = otel.Meter("go-rest-api/routes")

// authAttempts counts authentication decisions (outcome: success|denied, reason: <class>).
var authAttempts, _ = routesMeter.Int64Counter(
	"auth.attempts",
	metric.WithDescription("Total authentication/authorization decisions"),
	metric.WithUnit("{attempt}"),
)

// flowOutcomes counts end-to-end business flow completions (outcome: success|failure).
var flowOutcomes, _ = routesMeter.Int64Counter(
	"flow.outcomes",
	metric.WithDescription("Terminal outcome of each end-to-end request flow"),
	metric.WithUnit("{flow}"),
)

// flowDuration records end-to-end flow latency in seconds.
var flowDuration, _ = routesMeter.Float64Histogram(
	"flow.duration",
	metric.WithDescription("End-to-end request flow duration"),
	metric.WithUnit("s"),
)

// flowValidationOutcomes counts per-step validation results (outcome: passed|failed).
var flowValidationOutcomes, _ = routesMeter.Int64Counter(
	"flow.validation.outcomes",
	metric.WithDescription("Outcome of each request validation step"),
	metric.WithUnit("{validation}"),
)

// activeRequests tracks in-flight HTTP requests (UpDownCounter).
var activeRequests, _ = routesMeter.Int64UpDownCounter(
	"http.server.active_requests",
	metric.WithDescription("Number of in-flight HTTP requests"),
	metric.WithUnit("{request}"),
)

type ThingResp struct {
	Name string `json:"name"`
}

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	ctx, span := routesTracer.Start(req.Context(), "getThings")
	defer span.End()

	flowStart := time.Now()
	activeRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer activeRequests.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))

	// Validation step span
	_, valSpan := routesTracer.Start(ctx, "validate.getThings")
	valSpan.SetAttributes(attribute.String("validation.step", "getThings"), attribute.String("flow.id", span.SpanContext().TraceID().String()))
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	valSpan.End()
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "passed"),
		attribute.String("http.route", "/things"),
	))

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.ReturnJSON(resp, things)

	// Flow outcome and duration
	elapsed := time.Since(flowStart).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "success"),
		attribute.String("http.route", "/things"),
	))
	// P99 slow-request span event
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", otelTrace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things"),
		))
	}
}

// Get a thing by ID, dummy implementation
func (api ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	ctx, span := routesTracer.Start(req.Context(), "getThingByID")
	defer span.End()

	flowStart := time.Now()
	activeRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	defer activeRequests.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Validation step span
	_, valSpan := routesTracer.Start(ctx, "validate.getThingByID")
	valSpan.SetAttributes(attribute.String("validation.step", "getThingByID"), attribute.String("flow.id", span.SpanContext().TraceID().String()))

	// Example of using problem package to send a 404
	if id != "1" {
		valSpan.SetAttributes(attribute.String("validation.outcome", "failed"))
		valSpan.End()
		flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", "failed"),
			attribute.String("http.route", "/things/{id}"),
		))
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", "failure"),
			attribute.String("http.route", "/things/{id}"),
		))
		elapsed := time.Since(flowStart).Seconds()
		flowDuration.Record(ctx, elapsed, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failure"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	valSpan.End()
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "passed"),
		attribute.String("http.route", "/things/{id}"),
	))

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	api.ReturnJSON(resp, thing)

	// Flow outcome and duration
	elapsed := time.Since(flowStart).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "success"),
		attribute.String("http.route", "/things/{id}"),
	))
	// P99 slow-request span event
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", otelTrace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things/{id}"),
		))
	}
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	ctx, span := routesTracer.Start(req.Context(), "createThing")
	defer span.End()

	flowStart := time.Now()
	activeRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer activeRequests.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))

	// Validation step span
	_, valSpan := routesTracer.Start(ctx, "validate.createThing")
	valSpan.SetAttributes(attribute.String("validation.step", "createThing"), attribute.String("flow.id", span.SpanContext().TraceID().String()))
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	valSpan.End()
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "passed"),
		attribute.String("http.route", "/things"),
	))

	api.ReturnOKJSON(resp)

	// Flow outcome and duration
	elapsed := time.Since(flowStart).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "success"),
		attribute.String("http.route", "/things"),
	))
	// P99 slow-request span event
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", otelTrace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things"),
		))
	}
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	ctx, span := routesTracer.Start(req.Context(), "deleteThing")
	defer span.End()

	flowStart := time.Now()
	activeRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	defer activeRequests.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Validation step span
	_, valSpan := routesTracer.Start(ctx, "validate.deleteThing")
	valSpan.SetAttributes(attribute.String("validation.step", "deleteThing"), attribute.String("flow.id", span.SpanContext().TraceID().String()))

	// Example of using problem package to send a 404
	if id != "1" {
		valSpan.SetAttributes(attribute.String("validation.outcome", "failed"))
		valSpan.End()
		flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", "failed"),
			attribute.String("http.route", "/things/{id}"),
		))
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", "failure"),
			attribute.String("http.route", "/things/{id}"),
		))
		elapsed := time.Since(flowStart).Seconds()
		flowDuration.Record(ctx, elapsed, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failure"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	valSpan.End()
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "passed"),
		attribute.String("http.route", "/things/{id}"),
	))

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)

	// Flow outcome and duration
	elapsed := time.Since(flowStart).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "success"),
		attribute.String("http.route", "/things/{id}"),
	))
	// P99 slow-request span event
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", otelTrace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things/{id}"),
		))
	}
}
