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

	// flow.outcomes — counter for E2E business flow outcomes
	flowOutcomesCounter, _ = func() (metric.Int64Counter, error) {
		return routesMeter.Int64Counter(
			"flow.outcomes",
			metric.WithDescription("Terminal outcome of the primary business flow"),
			metric.WithUnit("{flow}"),
		)
	}()

	// flow.duration — histogram for E2E flow latency
	flowDurationHistogram, _ = func() (metric.Float64Histogram, error) {
		return routesMeter.Float64Histogram(
			"flow.duration",
			metric.WithDescription("End-to-end duration of the primary business flow"),
			metric.WithUnit("s"),
		)
	}()

	// flow.entry — counter for flow entry (throughput)
	flowEntryCounter, _ = func() (metric.Int64Counter, error) {
		return routesMeter.Int64Counter(
			"flow.entry",
			metric.WithDescription("Number of times the primary flow entry point is invoked"),
			metric.WithUnit("{flow}"),
		)
	}()

	// flow.validation.outcomes — counter for per-step validation outcomes
	flowValidationCounter, _ = func() (metric.Int64Counter, error) {
		return routesMeter.Int64Counter(
			"flow.validation.outcomes",
			metric.WithDescription("Outcome of each validation step in the primary flow"),
			metric.WithUnit("{validation}"),
		)
	}()
)

type ThingResp struct {
	Name string `json:"name"`
}

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	ctx, span := routesTracer.Start(req.Context(), "getThings")
	defer span.End()
	req = req.WithContext(ctx)

	start := time.Now()
	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))

	// Flow entry
	flowEntryCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.ReturnJSON(resp, things)

	// Flow outcome: success
	flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))

	// Flow duration
	elapsed := time.Since(start).Seconds()
	flowDurationHistogram.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things")))

	// Slow-request span event if P99 budget exceeded
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
	req = req.WithContext(ctx)

	start := time.Now()
	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	defer activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	flowEntryCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	id := chi.URLParam(req, "id")

	// Validation step span
	_, valSpan := routesTracer.Start(ctx, "validate.id")
	if id != "1" {
		valSpan.SetAttributes(attribute.String("validation.outcome", "failed"))
		valSpan.End()
		flowValidationCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failed"),
		))
		flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "not_found"),
		))
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	valSpan.End()
	flowValidationCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "passed"),
	))

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	api.ReturnJSON(resp, thing)

	flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))

	elapsed := time.Since(start).Seconds()
	flowDurationHistogram.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

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
	req = req.WithContext(ctx)

	start := time.Now()
	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))

	flowEntryCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("http.request.method", "POST"),
	))

	api.ReturnOKJSON(resp)

	flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "success"),
	))

	elapsed := time.Since(start).Seconds()
	flowDurationHistogram.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things")))

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
	req = req.WithContext(ctx)

	start := time.Now()
	activeRequestsCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	defer activeRequestsCounter.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	flowEntryCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("http.request.method", "DELETE"),
	))

	id := chi.URLParam(req, "id")

	_, valSpan := routesTracer.Start(ctx, "validate.id")
	if id != "1" {
		valSpan.SetAttributes(attribute.String("validation.outcome", "failed"))
		valSpan.End()
		flowValidationCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "failed"),
		))
		flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.String("outcome", "not_found"),
		))
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	valSpan.End()
	flowValidationCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "passed"),
	))

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")

	flowOutcomesCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
		attribute.String("outcome", "success"),
	))

	elapsed := time.Since(start).Seconds()
	flowDurationHistogram.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))

	if elapsed > p99BudgetSeconds {
		span.AddEvent("slow-request", otelTrace.WithAttributes(
			attribute.Float64("handler.duration_s", elapsed),
			attribute.String("http.route", "/things/{id}"),
		))
	}
}
