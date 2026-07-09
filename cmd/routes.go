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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type ThingResp struct {
	Name string `json:"name"`
}

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
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
	start := time.Now()
	recordFlowEntry(ctx)

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.String("error.type", "NotFoundError"))
		recordValidationOutcome(ctx, "not_found")
		recordFlowOutcome(ctx, "not_found", time.Since(start).Seconds())
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	recordValidationOutcome(ctx, "success")
	recordFlowOutcome(ctx, "success", time.Since(start).Seconds())
	api.ReturnJSON(resp, thing)
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	start := time.Now()
	recordFlowEntry(ctx)

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.String("error.type", "NotFoundError"))
		recordValidationOutcome(ctx, "not_found")
		recordFlowOutcome(ctx, "not_found", time.Since(start).Seconds())
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	recordValidationOutcome(ctx, "success")
	recordFlowOutcome(ctx, "success", time.Since(start).Seconds())

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")
}
