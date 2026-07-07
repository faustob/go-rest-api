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
	"github.com/benc-uk/go-rest-api/pkg/telemetry"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type ThingResp struct {
	Name string `json:"name"`
}

// Get all things, dummy implementation
func (api ThingAPI) getThings(resp http.ResponseWriter, req *http.Request) {
	flowStart := time.Now()
	ctx, span := telemetry.Tracer().Start(req.Context(), "flow.getThings",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	// Flow-entry counter (throughput SLI)
	telemetry.RecordFlowEntry(ctx, "/things")

	// Validation step span
	_, valSpan := telemetry.Tracer().Start(ctx, "flow.validation",
		trace.WithAttributes(attribute.String("flow.step", "input_validation")),
	)
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	telemetry.RecordValidationOutcome(ctx, "passed", "/things")
	valSpan.End()

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	// Flow outcome counter (success/availability SLI)
	telemetry.RecordFlowOutcome(ctx, "success", "/things")
	span.SetAttributes(attribute.String("flow.outcome", "success"))

	// Flow freshness histogram (entry-to-terminal duration)
	telemetry.RecordFlowDuration(ctx, time.Since(flowStart), "/things")

	// Slow-request span event for P99 triage (threshold: 750ms)
	if elapsed := time.Since(flowStart); elapsed > 750*time.Millisecond {
		span.AddEvent("slow_request", trace.WithAttributes(
			attribute.String("http.route", "/things"),
			attribute.Int64("handler.duration_ms", elapsed.Milliseconds()),
		))
	}

	api.ReturnJSON(resp, things)
}

// Get a thing by ID, dummy implementation
func (api ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	flowStart := time.Now()
	ctx, span := telemetry.Tracer().Start(req.Context(), "flow.getThingByID",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	// Flow-entry counter (throughput SLI)
	telemetry.RecordFlowEntry(ctx, "/things/{id}")

	id := chi.URLParam(req, "id")

	// Validation step span
	_, valSpan := telemetry.Tracer().Start(ctx, "flow.validation",
		trace.WithAttributes(attribute.String("flow.step", "id_validation")),
	)
	if id != "1" {
		valSpan.SetAttributes(attribute.String("validation.outcome", "failed"))
		telemetry.RecordValidationOutcome(ctx, "failed", "/things/{id}")
		valSpan.End()
		telemetry.RecordFlowOutcome(ctx, "failure", "/things/{id}")
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("flow.outcome", "failure"))
		telemetry.RecordFlowDuration(ctx, time.Since(flowStart), "/things/{id}")
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	telemetry.RecordValidationOutcome(ctx, "passed", "/things/{id}")
	valSpan.End()

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	// Flow outcome counter (success/availability SLI)
	telemetry.RecordFlowOutcome(ctx, "success", "/things/{id}")
	span.SetAttributes(attribute.String("flow.outcome", "success"))

	// Flow freshness histogram
	telemetry.RecordFlowDuration(ctx, time.Since(flowStart), "/things/{id}")

	// Slow-request span event for P99 triage
	if elapsed := time.Since(flowStart); elapsed > 750*time.Millisecond {
		span.AddEvent("slow_request", trace.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.Int64("handler.duration_ms", elapsed.Milliseconds()),
		))
	}

	api.ReturnJSON(resp, thing)
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	flowStart := time.Now()
	ctx, span := telemetry.Tracer().Start(req.Context(), "flow.createThing",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	// Flow-entry counter (throughput SLI)
	telemetry.RecordFlowEntry(ctx, "/things")

	// Validation step span
	_, valSpan := telemetry.Tracer().Start(ctx, "flow.validation",
		trace.WithAttributes(attribute.String("flow.step", "body_validation")),
	)
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	telemetry.RecordValidationOutcome(ctx, "passed", "/things")
	valSpan.End()

	// Flow outcome counter (success/availability SLI)
	telemetry.RecordFlowOutcome(ctx, "success", "/things")
	span.SetAttributes(attribute.String("flow.outcome", "success"))

	// Flow freshness histogram
	telemetry.RecordFlowDuration(ctx, time.Since(flowStart), "/things")

	// Slow-request span event for P99 triage
	if elapsed := time.Since(flowStart); elapsed > 750*time.Millisecond {
		span.AddEvent("slow_request", trace.WithAttributes(
			attribute.String("http.route", "/things"),
			attribute.Int64("handler.duration_ms", elapsed.Milliseconds()),
		))
	}

	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	flowStart := time.Now()
	ctx, span := telemetry.Tracer().Start(req.Context(), "flow.deleteThing",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	// Flow-entry counter (throughput SLI)
	telemetry.RecordFlowEntry(ctx, "/things/{id}")

	id := chi.URLParam(req, "id")

	// Validation step span
	_, valSpan := telemetry.Tracer().Start(ctx, "flow.validation",
		trace.WithAttributes(attribute.String("flow.step", "id_validation")),
	)
	if id != "1" {
		valSpan.SetAttributes(attribute.String("validation.outcome", "failed"))
		telemetry.RecordValidationOutcome(ctx, "failed", "/things/{id}")
		valSpan.End()
		telemetry.RecordFlowOutcome(ctx, "failure", "/things/{id}")
		span.SetStatus(codes.Error, "thing not found")
		span.SetAttributes(attribute.String("flow.outcome", "failure"))
		telemetry.RecordFlowDuration(ctx, time.Since(flowStart), "/things/{id}")
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}
	valSpan.SetAttributes(attribute.String("validation.outcome", "passed"))
	telemetry.RecordValidationOutcome(ctx, "passed", "/things/{id}")
	valSpan.End()

	// Flow outcome counter (success/availability SLI)
	telemetry.RecordFlowOutcome(ctx, "success", "/things/{id}")
	span.SetAttributes(attribute.String("flow.outcome", "success"))

	// Flow freshness histogram
	telemetry.RecordFlowDuration(ctx, time.Since(flowStart), "/things/{id}")

	// Slow-request span event for P99 triage
	if elapsed := time.Since(flowStart); elapsed > 750*time.Millisecond {
		span.AddEvent("slow_request", trace.WithAttributes(
			attribute.String("http.route", "/things/{id}"),
			attribute.Int64("handler.duration_ms", elapsed.Milliseconds()),
		))
	}

	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")
}
