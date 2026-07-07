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
	otelTrace "go.opentelemetry.io/otel/trace"
)

// p99BudgetSeconds is the P99 latency budget; a span event is added when exceeded.
const p99BudgetSeconds = 0.750

type ThingResp struct {
	Name string `json:"name"`
}

// routeMetrics holds per-package OTel instruments.
var (
	routesMeter         = otel.Meter("go-rest-api/routes")
	requestCounter      metric.Int64Counter
	activeRequestsGauge metric.Int64UpDownCounter
	flowOutcomeCounter  metric.Int64Counter
	flowDuration        metric.Float64Histogram
)

func init() {
	var err error
	requestCounter, err = routesMeter.Int64Counter(
		"http.server.requests.total",
		metric.WithDescription("Total HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(err)
	}

	activeRequestsGauge, err = routesMeter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		panic(err)
	}

	flowOutcomeCounter, err = routesMeter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of each end-to-end request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		panic(err)
	}

	flowDuration, err = routesMeter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end request flow duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(err)
	}
}

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx := req.Context()
	route := "/things"

	activeRequestsGauge.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route)))
	defer activeRequestsGauge.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", route)))

	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("flow.entry", "true"),
	))

	tracer := otel.Tracer("go-rest-api/routes")
	spanCtx, span := tracer.Start(ctx, "getThings")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", route),
			))
		}
		span.End()
		flowDuration.Record(spanCtx, elapsed, metric.WithAttributes(attribute.String("http.route", route)))
		flowOutcomeCounter.Add(spanCtx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", "success"),
		))
		requestCounter.Add(spanCtx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", "success"),
		))
	}()
	_ = spanCtx

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
	start := time.Now()
	ctx := req.Context()
	route := "/things/{id}"

	activeRequestsGauge.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route)))
	defer activeRequestsGauge.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", route)))

	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("flow.entry", "true"),
	))

	tracer := otel.Tracer("go-rest-api/routes")
	spanCtx, span := tracer.Start(ctx, "getThingByID")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", route),
			))
		}
		span.End()
		flowDuration.Record(spanCtx, elapsed, metric.WithAttributes(attribute.String("http.route", route)))
	}()

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		requestCounter.Add(spanCtx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", "not_found"),
		))
		flowOutcomeCounter.Add(spanCtx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", "not_found"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	requestCounter.Add(spanCtx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("outcome", "success"),
	))
	flowOutcomeCounter.Add(spanCtx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("outcome", "success"),
	))
	api.ReturnJSON(resp, thing)
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx := req.Context()
	route := "/things"

	activeRequestsGauge.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route)))
	defer activeRequestsGauge.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", route)))

	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("flow.entry", "true"),
	))

	tracer := otel.Tracer("go-rest-api/routes")
	spanCtx, span := tracer.Start(ctx, "createThing")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", route),
			))
		}
		span.End()
		flowDuration.Record(spanCtx, elapsed, metric.WithAttributes(attribute.String("http.route", route)))
		flowOutcomeCounter.Add(spanCtx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", "success"),
		))
		requestCounter.Add(spanCtx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", "success"),
		))
	}()
	_ = spanCtx

	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx := req.Context()
	route := "/things/{id}"

	activeRequestsGauge.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route)))
	defer activeRequestsGauge.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", route)))

	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("flow.entry", "true"),
	))

	tracer := otel.Tracer("go-rest-api/routes")
	spanCtx, span := tracer.Start(ctx, "deleteThing")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", route),
			))
		}
		span.End()
		flowDuration.Record(spanCtx, elapsed, metric.WithAttributes(attribute.String("http.route", route)))
	}()

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		requestCounter.Add(spanCtx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", "not_found"),
		))
		flowOutcomeCounter.Add(spanCtx, 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", "not_found"),
		))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	requestCounter.Add(spanCtx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("outcome", "success"),
	))
	flowOutcomeCounter.Add(spanCtx, 1, metric.WithAttributes(
		attribute.String("http.route", route),
		attribute.String("outcome", "success"),
	))

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")
}
