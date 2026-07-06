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

// p99BudgetSeconds is the P99 latency budget; a span event is added when exceeded.
const p99BudgetSeconds = 0.750

var (
	routesTracer = otel.Tracer("go-rest-api/routes")
	routesMeter  = otel.Meter("go-rest-api/routes")

	// http.server.active_requests — UpDownCounter for in-flight requests
	activeRequestsCounter, _ = func() (metric.Int64UpDownCounter, error) {
		return routesMeter.Int64UpDownCounter(
			"http.server.active_requests",
			metric.WithDescription("Number of in-flight HTTP requests"),
			metric.WithUnit("{request}"),
		)
	}()

	// flow.outcomes — counter for E2E business flow terminal outcomes
	flowOutcomesCounter, _ = func() (metric.Int64Counter, error) {
		return routesMeter.Int64Counter(
			"flow.outcomes",
			metric.WithDescription("Terminal outcome of each E2E business flow"),
			metric.WithUnit("{flow}"),
		)
	}()

	// flow.duration — histogram for E2E flow entry-to-terminal duration
	flowDurationHistogram, _ = func() (metric.Float64Histogram, error) {
		return routesMeter.Float64Histogram(
			"flow.duration",
			metric.WithDescription("End-to-end duration of a business flow"),
			metric.WithUnit("s"),
		)
	}()
)

type ThingResp struct {
	Name string `json:"name"`
}

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	flowStart := time.Now()
	outcome := "success"

	ctx, span := routesTracer.Start(ctx, "getThings")
	defer func() {
		elapsed := time.Since(flowStart).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", "/things"),
			))
		}
		span.End()
		flowDurationHistogram.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things")))
		flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things"),
			attribute.String("outcome", outcome),
		))
	}()

	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.ReturnJSON(resp, things)
}

// Get a thing by ID, dummy implementation
func (api ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	flowStart := time.Now()
	const route = "/things/{id}"
	outcome := "success"

	ctx, span := routesTracer.Start(ctx, "getThingByID")
	defer func() {
		elapsed := time.Since(flowStart).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", route),
			))
		}
		span.End()
		flowDurationHistogram.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", route)))
		flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
		))
	}()

	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route)))
	defer activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", route)))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		outcome = "not_found"
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	api.ReturnJSON(resp, thing)
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	flowStart := time.Now()
	const route = "/things"
	outcome := "success"

	ctx, span := routesTracer.Start(ctx, "createThing")
	defer func() {
		elapsed := time.Since(flowStart).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", route),
			))
		}
		span.End()
		flowDurationHistogram.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", route)))
		flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
		))
	}()

	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route)))
	defer activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", route)))

	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	flowStart := time.Now()
	const route = "/things/{id}"
	outcome := "success"

	ctx, span := routesTracer.Start(ctx, "deleteThing")
	defer func() {
		elapsed := time.Since(flowStart).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", route),
			))
		}
		span.End()
		flowDurationHistogram.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", route)))
		flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
		))
	}()

	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route)))
	defer activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", route)))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		outcome = "not_found"
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")
}
