// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Some example routes for the thing API
// ----------------------------------------------------------------------------

package main

import (
	"context"
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

// p99BudgetSeconds is the P99 latency SLO budget; a span event is added when exceeded.
const p99BudgetSeconds = 0.750

// routeTracer and routeMeter are package-level OTel instruments for route handlers.
var (
	routeTracer = otel.Tracer("go-rest-api/routes")
	routeMeter  = otel.Meter("go-rest-api/routes")

	// http.server.request.duration is emitted by otelhttp middleware; we add a
	// flow-level histogram for E2E business-flow latency and freshness.
	flowDuration metric.Float64Histogram
	flowOutcomes metric.Int64Counter
	flowEntries  metric.Int64Counter

	// auth attempt counter
	authAttempts metric.Int64Counter
)

func init() {
	var err error
	flowDuration, err = routeMeter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end business flow duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(err)
	}
	flowOutcomes, err = routeMeter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of a business flow"),
	)
	if err != nil {
		panic(err)
	}
	flowEntries, err = routeMeter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Number of times the primary flow entry point is invoked"),
	)
	if err != nil {
		panic(err)
	}
	authAttempts, err = routeMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions"),
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
	start := time.Now()
	ctx, span := routeTracer.Start(req.Context(), "getThings")
	defer span.End()
	req = req.WithContext(ctx)

	// Flow entry counter — every invocation of the primary list endpoint.
	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.ReturnJSON(resp, things)

	// Record E2E flow duration and outcome.
	elapsed := time.Since(start).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))

	// Slow-request span event when P99 budget is exceeded.
	if elapsed > p99BudgetSeconds {
		span.AddEvent("p99.budget.exceeded", trace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things"),
		))
	}
}

// Get a thing by ID, dummy implementation
func (api ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx, span := routeTracer.Start(req.Context(), "getThingByID")
	defer span.End()
	req = req.WithContext(ctx)

	// Flow entry counter.
	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "not_found"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	api.ReturnJSON(resp, thing)

	elapsed := time.Since(start).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))

	if elapsed > p99BudgetSeconds {
		span.AddEvent("p99.budget.exceeded", trace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things/{id}"),
		))
	}
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx, span := routeTracer.Start(req.Context(), "createThing")
	defer span.End()
	req = req.WithContext(ctx)

	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))

	api.ReturnOKJSON(resp)

	elapsed := time.Since(start).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))

	if elapsed > p99BudgetSeconds {
		span.AddEvent("p99.budget.exceeded", trace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things"),
		))
	}
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx, span := routeTracer.Start(req.Context(), "deleteThing")
	defer span.End()
	req = req.WithContext(ctx)

	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "not_found"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")

	elapsed := time.Since(start).Seconds()
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))

	if elapsed > p99BudgetSeconds {
		span.AddEvent("p99.budget.exceeded", trace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things/{id}"),
		))
	}
}
