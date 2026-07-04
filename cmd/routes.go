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
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	tracer := otel.Tracer("go-rest-api")
	ctx, span := tracer.Start(req.Context(), "getThings")
	req = req.WithContext(ctx)
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", trace.WithAttributes(
				attribute.String("http.route", "/things"),
				attribute.Float64("handler.duration_s", elapsed),
			))
		}
		span.End()
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
	start := time.Now()
	tracer := otel.Tracer("go-rest-api")
	ctx, span := tracer.Start(req.Context(), "getThingByID")
	req = req.WithContext(ctx)
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", trace.WithAttributes(
				attribute.String("http.route", "/things/{id}"),
				attribute.Float64("handler.duration_s", elapsed),
			))
		}
		span.End()
	}()

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
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
	start := time.Now()
	tracer := otel.Tracer("go-rest-api")
	_, span := tracer.Start(req.Context(), "createThing")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", trace.WithAttributes(
				attribute.String("http.route", "/things"),
				attribute.Float64("handler.duration_s", elapsed),
			))
		}
		span.End()
	}()
	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	start := time.Now()
	tracer := otel.Tracer("go-rest-api")
	_, span := tracer.Start(req.Context(), "deleteThing")
	defer func() {
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span.AddEvent("slow-request", trace.WithAttributes(
				attribute.String("http.route", "/things/{id}"),
				attribute.Float64("handler.duration_s", elapsed),
			))
		}
		span.End()
	}()

	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")
}
