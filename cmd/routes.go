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
	ctx := req.Context()
	tracer := otel.Tracer("go-rest-api")
	ctx, span := tracer.Start(ctx, "getThings", trace.WithAttributes(
		attribute.String("http.route", "/things"),
	))
	defer span.End()

	start := time.Now()
	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	elapsed := time.Since(start)
	const p99Budget = 750 * time.Millisecond
	if elapsed > p99Budget {
		span.AddEvent("slow-request", trace.WithAttributes(
			attribute.String("http.route", "/things"),
			attribute.Int64("handler.duration_ms", elapsed.Milliseconds()),
		))
	}

	_ = ctx
	api.ReturnJSON(resp, things)
}

// Get a thing by ID, dummy implementation
func (api ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer("go-rest-api")
	ctx, span := tracer.Start(ctx, "getThingByID", trace.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
	))
	defer span.End()

	start := time.Now()
	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	elapsed := time.Since(start)
	const p99Budget = 750 * time.Millisecond
	if elapsed > p99Budget {
		span.AddEvent("slow-request", trace.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.Int64("handler.duration_ms", elapsed.Milliseconds()),
		))
	}

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	_ = ctx
	api.ReturnJSON(resp, thing)
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer("go-rest-api")
	ctx, span := tracer.Start(ctx, "createThing", trace.WithAttributes(
		attribute.String("http.route", "/things"),
	))
	defer span.End()

	start := time.Now()

	api.ReturnOKJSON(resp)

	elapsed := time.Since(start)
	const p99Budget = 750 * time.Millisecond
	if elapsed > p99Budget {
		span.AddEvent("slow-request", trace.WithAttributes(
			attribute.String("http.route", "/things"),
			attribute.Int64("handler.duration_ms", elapsed.Milliseconds()),
		))
	}

	_ = ctx
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tracer := otel.Tracer("go-rest-api")
	ctx, span := tracer.Start(ctx, "deleteThing", trace.WithAttributes(
		attribute.String("http.route", "/things/{id}"),
	))
	defer span.End()

	start := time.Now()
	id := chi.URLParam(req, "id")

	// Example of using problem package to send a 404
	if id != "1" {
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("error.type", "not_found"))
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	elapsed := time.Since(start)
	const p99Budget = 750 * time.Millisecond
	if elapsed > p99Budget {
		span.AddEvent("slow-request", trace.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.Int64("handler.duration_ms", elapsed.Milliseconds()),
		))
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")

	_ = ctx
}
