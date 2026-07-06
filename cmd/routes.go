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
	trace "go.opentelemetry.io/otel/trace"
)

type ThingResp struct {
	Name string `json:"name"`
}

// p99BudgetSeconds is the P99 latency budget; a span event is added when a handler exceeds it.
const p99BudgetSeconds = 0.750

// routesMeter is the OTel meter for route-level business metrics.
var routesMeter = otel.Meter("go-rest-api/routes")

// flowOutcomes counts end-to-end flow completions by outcome.
var flowOutcomes, _ = routesMeter.Int64Counter(
	"flow.outcomes",
	metric.WithDescription("Terminal outcome of each request flow"),
	metric.WithUnit("{flow}"),
)

// flowDuration records end-to-end flow latency in seconds.
var flowDuration, _ = routesMeter.Float64Histogram(
	"flow.duration",
	metric.WithDescription("End-to-end request flow duration"),
	metric.WithUnit("s"),
)

// activeRequests tracks in-flight HTTP requests (UpDownCounter).
var activeRequests, _ = routesMeter.Int64UpDownCounter(
	"http.server.active_requests",
	metric.WithDescription("Number of in-flight HTTP requests"),
	metric.WithUnit("{request}"),
)

// routesTracer is the OTel tracer for route-level spans.
var routesTracer = otel.Tracer("go-rest-api/routes")

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	ctx, span := routesTracer.Start(req.Context(), "getThings")
	defer span.End()

	start := time.Now()
	activeRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer activeRequests.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.ReturnJSON(resp, things)

	elapsed := time.Since(start).Seconds()
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things"),
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

	start := time.Now()
	activeRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	defer activeRequests.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "not_found"),
		))
		flowDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	api.ReturnJSON(resp, thing)

	elapsed := time.Since(start).Seconds()
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
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

	start := time.Now()
	activeRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer activeRequests.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))

	api.ReturnOKJSON(resp)

	elapsed := time.Since(start).Seconds()
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things"),
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

	start := time.Now()
	activeRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	defer activeRequests.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "not_found"),
		))
		flowDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")

	elapsed := time.Since(start).Seconds()
	flowOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))
	flowDuration.Record(ctx, elapsed, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
	))
	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", trace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things/{id}"),
		))
	}
}
