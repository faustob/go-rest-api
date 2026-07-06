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

// p99BudgetSeconds is the P99 latency SLO budget; a span event is added when exceeded.
const p99BudgetSeconds = 0.750

// flowDurationHistogram records end-to-end flow (entry-to-terminal) duration in seconds.
// activeRequestsCounter tracks in-flight requests (UpDownCounter).
// flowOutcomeCounter counts flow outcomes by result.
var (
	routesMeter         = otel.Meter("go-rest-api/routes")
	flowDurationHist    metric.Float64Histogram
	activeRequestsGauge metric.Int64UpDownCounter
	flowOutcomeCounter  metric.Int64Counter
)

func init() {
	var err error
	flowDurationHist, err = routesMeter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end request flow duration"),
		metric.WithUnit("s"),
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
		metric.WithDescription("Terminal outcome of each request flow"),
		metric.WithUnit("{flow}"),
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
	ctx := req.Context()
	tracer := otel.Tracer("go-rest-api/routes")
	ctx, span := tracer.Start(ctx, "getThings")
	defer span.End()

	flowStart := time.Now()
	activeRequestsGauge.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer func() {
		activeRequestsGauge.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))
		elapsed := time.Since(flowStart).Seconds()
		flowDurationHist.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things")))
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", "/things"),
			))
		}
		flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success"), attribute.String("http.route", "/things")))
	}()

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
	tracer := otel.Tracer("go-rest-api/routes")
	ctx, span := tracer.Start(ctx, "getThingByID")
	defer span.End()

	flowStart := time.Now()
	activeRequestsGauge.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	defer func() {
		activeRequestsGauge.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
		elapsed := time.Since(flowStart).Seconds()
		flowDurationHist.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", "/things/{id}"),
			))
		}
	}()

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "not_found"), attribute.String("http.route", "/things/{id}")))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success"), attribute.String("http.route", "/things/{id}")))
	api.ReturnJSON(resp, thing)
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer("go-rest-api/routes")
	ctx, span := tracer.Start(ctx, "createThing")
	defer span.End()

	flowStart := time.Now()
	activeRequestsGauge.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))
	defer func() {
		activeRequestsGauge.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things")))
		elapsed := time.Since(flowStart).Seconds()
		flowDurationHist.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things"), attribute.String("http.request.method", "POST")))
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", "/things"),
			))
		}
		flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success"), attribute.String("http.route", "/things"), attribute.String("http.request.method", "POST")))
	}()

	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer("go-rest-api/routes")
	ctx, span := tracer.Start(ctx, "deleteThing")
	defer span.End()

	flowStart := time.Now()
	activeRequestsGauge.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
	defer func() {
		activeRequestsGauge.Add(ctx, -1, metric.WithAttributes(attribute.String("http.route", "/things/{id}")))
		elapsed := time.Since(flowStart).Seconds()
		flowDurationHist.Record(ctx, elapsed, metric.WithAttributes(attribute.String("http.route", "/things/{id}"), attribute.String("http.request.method", "DELETE")))
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", otelTrace.WithAttributes(
				attribute.Float64("handler.duration_s", elapsed),
				attribute.String("http.route", "/things/{id}"),
			))
		}
	}()

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "not_found"), attribute.String("http.route", "/things/{id}"), attribute.String("http.request.method", "DELETE")))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	flowOutcomeCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success"), attribute.String("http.route", "/things/{id}"), attribute.String("http.request.method", "DELETE")))
	api.ReturnText(resp, "Thing deleted")
}
