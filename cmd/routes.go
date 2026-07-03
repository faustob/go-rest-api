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
	"go.opentelemetry.io/otel/trace"
)

type ThingResp struct {
	Name string `json:"name"`
}

// Get all things, dummy implementation
func (api *ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(ctx, "getThings", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	flowStart := time.Now()
	api.RecordFlowEntry(ctx)

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	api.RecordFlowOutcome(ctx, "success", time.Since(flowStart).Seconds())
	api.RecordValidationOutcome(ctx, "list", "passed")
	api.ReturnJSON(resp, things)
}

// Get a thing by ID, dummy implementation
func (api *ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(ctx, "getThingByID", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	flowStart := time.Now()
	api.RecordFlowEntry(ctx)

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		api.RecordFlowOutcome(ctx, "failure", time.Since(flowStart).Seconds())
		api.RecordValidationOutcome(ctx, "get_by_id", "failed")
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	api.RecordFlowOutcome(ctx, "success", time.Since(flowStart).Seconds())
	api.RecordValidationOutcome(ctx, "get_by_id", "passed")
	api.ReturnJSON(resp, thing)
}

// Create a new thing, dummy implementation
func (api *ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(ctx, "createThing", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	flowStart := time.Now()
	api.RecordFlowEntry(ctx)
	api.RecordValidationOutcome(ctx, "create", "passed")
	api.RecordFlowOutcome(ctx, "success", time.Since(flowStart).Seconds())
	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api *ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer(serviceName)
	ctx, span := tracer.Start(ctx, "deleteThing", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	flowStart := time.Now()
	api.RecordFlowEntry(ctx)

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		api.RecordFlowOutcome(ctx, "failure", time.Since(flowStart).Seconds())
		api.RecordValidationOutcome(ctx, "delete", "failed")
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.RecordFlowOutcome(ctx, "success", time.Since(flowStart).Seconds())
	api.RecordValidationOutcome(ctx, "delete", "passed")
	api.ReturnText(resp, "Thing deleted")
}
