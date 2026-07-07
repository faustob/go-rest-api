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
	// Flow entry: increment flow throughput counter and start flow span.
	flowStart := time.Now()
	telemetry.FlowEntryCounter.Add(req.Context(), 1,
		telemetry.AttrFlowRoute("/things"),
	)
	telemetry.TenantRequestCounter.Add(req.Context(), 1,
		telemetry.AttrTenant(req),
		telemetry.AttrFlowRoute("/things"),
	)

	ctx, flowSpan := telemetry.Tracer.Start(req.Context(), "flow.get_things",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer func() {
		// Flow freshness: wall-clock entry-to-terminal duration.
		telemetry.FlowDurationHistogram.Record(ctx, time.Since(flowStart).Seconds())
		flowSpan.End()
	}()

	things := make([]ThingResp, 0)

	things = append(things, ThingResp{
		Name: "Cheese On Toast",
	})
	things = append(things, ThingResp{
		Name: "Bacon Sandwich",
	})

	telemetry.FlowOutcomeCounter.Add(ctx, 1,
		attribute.String("outcome", "success"),
		telemetry.AttrFlowRoute("/things"),
	)
	flowSpan.SetStatus(codes.Ok, "")
	api.ReturnJSON(resp, things)
}

// Get a thing by ID, dummy implementation
func (api ThingAPI) getThingByID(resp http.ResponseWriter, req *http.Request) {
	flowStart := time.Now()
	telemetry.FlowEntryCounter.Add(req.Context(), 1,
		telemetry.AttrFlowRoute("/things/{id}"),
	)
	telemetry.TenantRequestCounter.Add(req.Context(), 1,
		telemetry.AttrTenant(req),
		telemetry.AttrFlowRoute("/things/{id}"),
	)

	ctx, flowSpan := telemetry.Tracer.Start(req.Context(), "flow.get_thing_by_id",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer func() {
		telemetry.FlowDurationHistogram.Record(ctx, time.Since(flowStart).Seconds())
		flowSpan.End()
	}()

	id := chi.URLParam(req, "id")

	// Validation step span.
	_, valSpan := telemetry.Tracer.Start(ctx, "flow.validate_thing_id")

	// Example of using problem package to send a 404
	if id != "1" {
		valSpan.SetAttributes(attribute.String("validation.result", "failed"), attribute.String("flow.id", id))
		valSpan.End()
		telemetry.ValidationOutcomeCounter.Add(ctx, 1,
			attribute.String("outcome", "failed"),
			attribute.String("step", "id_check"),
		)
		telemetry.FlowOutcomeCounter.Add(ctx, 1,
			attribute.String("outcome", "failure"),
			telemetry.AttrFlowRoute("/things/{id}"),
		)
		flowSpan.SetStatus(codes.Error, "thing not found")
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	valSpan.SetAttributes(attribute.String("validation.result", "passed"), attribute.String("flow.id", id))
	valSpan.End()
	telemetry.ValidationOutcomeCounter.Add(ctx, 1,
		attribute.String("outcome", "passed"),
		attribute.String("step", "id_check"),
	)

	thing := ThingResp{
		Name: "Cheese On Toast",
	}

	telemetry.FlowOutcomeCounter.Add(ctx, 1,
		attribute.String("outcome", "success"),
		telemetry.AttrFlowRoute("/things/{id}"),
	)
	flowSpan.SetStatus(codes.Ok, "")
	api.ReturnJSON(resp, thing)
}

// Create a new thing, dummy implementation
func (api ThingAPI) createThing(resp http.ResponseWriter, req *http.Request) {
	flowStart := time.Now()
	telemetry.FlowEntryCounter.Add(req.Context(), 1,
		telemetry.AttrFlowRoute("/things"),
	)
	telemetry.TenantRequestCounter.Add(req.Context(), 1,
		telemetry.AttrTenant(req),
		telemetry.AttrFlowRoute("/things"),
	)

	ctx, flowSpan := telemetry.Tracer.Start(req.Context(), "flow.create_thing",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer func() {
		telemetry.FlowDurationHistogram.Record(ctx, time.Since(flowStart).Seconds())
		flowSpan.End()
	}()

	telemetry.FlowOutcomeCounter.Add(ctx, 1,
		attribute.String("outcome", "success"),
		telemetry.AttrFlowRoute("/things"),
	)
	flowSpan.SetStatus(codes.Ok, "")
	api.ReturnOKJSON(resp)
}

// Delete a thing by ID, dummy implementation
func (api ThingAPI) deleteThing(resp http.ResponseWriter, req *http.Request) {
	flowStart := time.Now()
	telemetry.FlowEntryCounter.Add(req.Context(), 1,
		telemetry.AttrFlowRoute("/things/{id}"),
	)
	telemetry.TenantRequestCounter.Add(req.Context(), 1,
		telemetry.AttrTenant(req),
		telemetry.AttrFlowRoute("/things/{id}"),
	)

	ctx, flowSpan := telemetry.Tracer.Start(req.Context(), "flow.delete_thing",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer func() {
		telemetry.FlowDurationHistogram.Record(ctx, time.Since(flowStart).Seconds())
		flowSpan.End()
	}()

	id := chi.URLParam(req, "id")

	// Validation step span.
	_, valSpan := telemetry.Tracer.Start(ctx, "flow.validate_delete_id")

	// Example of using problem package to send a 404
	if id != "1" {
		valSpan.SetAttributes(attribute.String("validation.result", "failed"), attribute.String("flow.id", id))
		valSpan.End()
		telemetry.ValidationOutcomeCounter.Add(ctx, 1,
			attribute.String("outcome", "failed"),
			attribute.String("step", "id_check"),
		)
		telemetry.FlowOutcomeCounter.Add(ctx, 1,
			attribute.String("outcome", "failure"),
			telemetry.AttrFlowRoute("/things/{id}"),
		)
		flowSpan.SetStatus(codes.Error, "thing not found")
		problem.Wrap(404, req.RequestURI, "thing", errors.New("thing not found")).Send(resp)
		return
	}

	valSpan.SetAttributes(attribute.String("validation.result", "passed"), attribute.String("flow.id", id))
	valSpan.End()
	telemetry.ValidationOutcomeCounter.Add(ctx, 1,
		attribute.String("outcome", "passed"),
		attribute.String("step", "id_check"),
	)

	telemetry.FlowOutcomeCounter.Add(ctx, 1,
		attribute.String("outcome", "success"),
		telemetry.AttrFlowRoute("/things/{id}"),
	)
	flowSpan.SetStatus(codes.Ok, "")
	// Send a 204 No Content response
	resp.WriteHeader(http.StatusNoContent)
	api.ReturnText(resp, "Thing deleted")
}
