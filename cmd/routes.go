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
	"go.opentelemetry.io/otel/metric"
)

type ThingResp struct {
	Name string `json:"name"`
}

// p99BudgetSeconds is the P99 latency SLO budget (750 ms).
const p99BudgetSeconds = 0.750

// routesMeter holds the OTel meter for route-level instruments.
var routesMeter = otel.Meter("go-rest-api/routes")

// flowOutcomes counts E2E business flow terminal outcomes.
var flowOutcomes, _ = routesMeter.Int64Counter(
	"flow.outcomes",
	metric.WithDescription("Terminal outcome of the primary request flow"),
	metric.WithUnit("{request}"),
)

// flowDuration records E2E flow latency for freshness / P95 SLO.
var flowDuration, _ = routesMeter.Float64Histogram(
	"flow.duration",
	metric.WithDescription("End-to-end duration of the primary request flow"),
	metric.WithUnit("s"),
)

// flowEntries counts every flow entry for throughput SLO.
var flowEntries, _ = routesMeter.Int64Counter(
	"flow.entries",
	metric.WithDescription("Number of times the primary flow entry point was invoked"),
	metric.WithUnit("{request}"),
)

// validationOutcomes counts per-step validation pass/fail for the validation-failure-rate SLO.
var validationOutcomes, _ = routesMeter.Int64Counter(
	"flow.validation.outcomes",
	metric.WithDescription("Outcome of each request validation step"),
	metric.WithUnit("{validation}"),
)

// activeRequests tracks in-flight requests for the saturation SLO.
var activeRequests int64

func init() {
	// Register observable gauges for saturation SLO.
	ar, err := routesMeter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return
	}
	_, err = routesMeter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(ar, activeRequests)
		return nil
	}, ar)
	if err != nil {
		return
	}
}

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer("go-rest-api/routes")
	ctx, span := tracer.Start(ctx, "flow.getThings")
	defer span.End()

	start := time.Now()
	flowEntries.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", "/things")))

	// Track active requests for saturation SLO
	activeRequests++
	defer func() { activeRequests-- }()

	// Validation step span
	_, valSpan := tracer.Start(ctx, "flow.validation.getThings")
	validationOutcomes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.route", "/things"),
		attribute.String("outcome", "passed"),
	))
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	valSpan.End()

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.ReturnJSON(resp, things)

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
		span.AddEvent("slow_request", otel.Tracer("go-rest-api/routes").Start) // placeholder replaced below
		span.AddEvent("slow_request",
			trace.WithAttributes(
				attribute.Float64("elapsed_s", elapsed),
				attribute.String("http.route", "/things"),
			),
		)
	}
}

// Get a thing by ID, dummy implementation
func (api ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
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
	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")
}
