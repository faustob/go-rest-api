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
	"go.opentelemetry.io/otel/trace"
)

// p99BudgetSeconds is the P99 latency budget; a span event is added when exceeded.
const p99BudgetSeconds = 0.750

var (
	routesMeter   = otel.Meter("go-rest-api/routes")
	routesTracer  = otel.Tracer("go-rest-api/routes")

	// flow.outcomes counter — E2E business flow success/failure
	flowOutcomes metric.Int64Counter
	// flow.duration histogram — E2E flow latency
	flowDuration metric.Float64Histogram
	// flow.validation.outcomes counter — per-step validation pass/fail
	flowValidationOutcomes metric.Int64Counter
	// flow.entries counter — flow throughput
	flowEntries metric.Int64Counter
)

func init() {
	var err error
	flowOutcomes, err = routesMeter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of the primary request flow"),
	)
	if err != nil {
		panic(err)
	}
	flowDuration, err = routesMeter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary request flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(err)
	}
	flowValidationOutcomes, err = routesMeter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcome of each validation step in the primary flow"),
	)
	if err != nil {
		panic(err)
	}
	flowEntries, err = routesMeter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Number of times the primary flow entry point is invoked"),
	)
	if err != nil {
		panic(err)
	}
}

type ThingResp struct {
	Name string `json:"name"`
}

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	ctx, span := routesTracer.Start(req.Context(), "getThings")
	defer span.End()
	flowStart := time.Now()
	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.ReturnJSON(resp, things)

	elapsed := time.Since(flowStart).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", trace.WithAttributes(
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
	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("step", "id-lookup"),
		attribute.String("outcome", "attempted"),
	))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("step", "id-lookup"),
			attribute.String("outcome", "failed"),
		))
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
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

	elapsed := time.Since(flowStart).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("step", "id-lookup"),
		attribute.String("outcome", "passed"),
	))
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", trace.WithAttributes(
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
	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))

	api.ReturnOKJSON(resp)

	elapsed := time.Since(flowStart).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", trace.WithAttributes(
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
	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failure"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")

	elapsed := time.Since(flowStart).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", trace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things/{id}"),
		))
	}
}
