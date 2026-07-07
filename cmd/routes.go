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
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type ThingResp struct {
	Name string `json:"name"`
}

// p99BudgetSeconds is the P99 latency budget; a span event is added when exceeded.
const p99BudgetSeconds = 0.750

// routesMeter holds the OTel meter for route-level custom metrics.
var routesMeter = otel.Meter("go-rest-api/routes")

// routesTracer is the tracer for route-level spans.
var routesTracer = otel.Tracer("go-rest-api/routes")

// flowOutcomes counts E2E business flow terminal outcomes (success/failure).
var flowOutcomes, _ = routesMeter.Int64Counter(
	"flow.outcomes",
	metric.WithDescription("Terminal outcome of the primary E2E business flow"),
	metric.WithUnit("{request}"),
)

// flowDuration records E2E flow latency for freshness / P95 SLI.
var flowDuration, _ = routesMeter.Float64Histogram(
	"flow.duration",
	metric.WithDescription("End-to-end duration of the primary business flow"),
	metric.WithUnit("s"),
)

// flowEntries counts every flow entry invocation for throughput SLI.
var flowEntries, _ = routesMeter.Int64Counter(
	"flow.entries",
	metric.WithDescription("Number of times the primary flow entry point was invoked"),
	metric.WithUnit("{request}"),
)

// flowValidationOutcomes counts per-step validation pass/fail for validation-failure-rate SLI.
var flowValidationOutcomes, _ = routesMeter.Int64Counter(
	"flow.validation.outcomes",
	metric.WithDescription("Outcome of each validation step in the primary flow"),
	metric.WithUnit("{validation}"),
)

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx, span := routesTracer.Start(req.Context(), "getThings")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("p99-budget-exceeded",
				trace.WithAttributes(attribute.Float64("handler.duration_s", elapsed)),
			)
		}
		span.End()
	}()
	_ = ctx

	flowEntries.Add(req.Context(), 1, metric.WithAttributes(attribute.String("http.route", "/things")))

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.ReturnJSON(resp, things)

	elapsedFinal := time.Since(start).Seconds()
	flowDuration.Record(req.Context(), elapsedFinal, metric.WithAttributes(attribute.String("http.route", "/things")))
	flowOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowValidationOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "passed"),
	))
}

// Get a thing by ID, dummy implementation
func (api ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx, span := routesTracer.Start(req.Context(), "getThingByID")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("p99-budget-exceeded",
				trace.WithAttributes(attribute.Float64("handler.duration_s", elapsed)),
			)
		}
		span.End()
	}()
	_ = ctx

	flowEntries.Add(req.Context(), 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		flowValidationOutcomes.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failed"),
		))
		flowOutcomes.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failure"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	api.ReturnJSON(resp, thing)

	elapsedFinal := time.Since(start).Seconds()
	flowDuration.Record(req.Context(), elapsedFinal, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	flowOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowValidationOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "passed"),
	))
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx, span := routesTracer.Start(req.Context(), "createThing")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("p99-budget-exceeded",
				trace.WithAttributes(attribute.Float64("handler.duration_s", elapsed)),
			)
		}
		span.End()
	}()
	_ = ctx

	flowEntries.Add(req.Context(), 1, metric.WithAttributes(attribute.String("http.route", "/things")))

	api.ReturnOKJSON(resp)

	elapsedFinal := time.Since(start).Seconds()
	flowDuration.Record(req.Context(), elapsedFinal, metric.WithAttributes(attribute.String("http.route", "/things")))
	flowOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowValidationOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "passed"),
	))
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx, span := routesTracer.Start(req.Context(), "deleteThing")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("p99-budget-exceeded",
				trace.WithAttributes(attribute.Float64("handler.duration_s", elapsed)),
			)
		}
		span.End()
	}()
	_ = ctx

	flowEntries.Add(req.Context(), 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		flowValidationOutcomes.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failed"),
		))
		flowOutcomes.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failure"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")

	elapsedFinal := time.Since(start).Seconds()
	flowDuration.Record(req.Context(), elapsedFinal, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	flowOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowValidationOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "passed"),
	))
}
