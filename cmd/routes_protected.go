// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Protected and public route registration for the ThingAPI.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// statusRecorder wraps http.ResponseWriter to capture the written status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}

func outcomeFromStatus(code int) string {
	if code >= 200 && code < 400 {
		return "success"
	}
	if code >= 400 && code < 500 {
		return "client_error"
	}
	return "server_error"
}

// getThingsInstrumented wraps getThings with flow telemetry.
func (api ThingAPI) getThingsInstrumented(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("github.com/benc-uk/go-rest-api")
	ctx, span := tracer.Start(r.Context(), "flow.getThings",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	start := time.Now()

	if api.tel != nil {
		api.tel.flowEntries.Add(ctx, 1)
	}

	// validation step span
	_, valSpan := tracer.Start(ctx, "flow.validation.getThings")
	valSpan.SetAttributes(attribute.String("validation.step", "input"))
	valSpan.SetAttributes(attribute.String("outcome", "passed"))
	valSpan.End()

	if api.tel != nil {
		api.tel.flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(attrStep.String("input"), attrOutcome.String("passed")))
	}

	sr := newStatusRecorder(w)
	api.getThings(sr, r.WithContext(ctx))

	elapsed := time.Since(start).Seconds()
	if api.tel != nil {
		api.tel.flowOutcomes.Add(ctx, 1, metric.WithAttributes(attrOutcome.String(outcomeFromStatus(sr.statusCode))))
		api.tel.flowDuration.Record(ctx, elapsed)
	}
}

// getThingByIDInstrumented wraps getThingByID with flow telemetry.
func (api ThingAPI) getThingByIDInstrumented(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("github.com/benc-uk/go-rest-api")
	ctx, span := tracer.Start(r.Context(), "flow.getThingByID",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	start := time.Now()

	if api.tel != nil {
		api.tel.flowEntries.Add(ctx, 1)
	}

	// validation step span
	_, valSpan := tracer.Start(ctx, "flow.validation.getThingByID")
	valSpan.SetAttributes(attribute.String("validation.step", "id-param"))
	valSpan.SetAttributes(attribute.String("outcome", "passed"))
	valSpan.End()

	if api.tel != nil {
		api.tel.flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(attrStep.String("id-param"), attrOutcome.String("passed")))
	}

	sr := newStatusRecorder(w)
	api.getThingByID(sr, r.WithContext(ctx))

	elapsed := time.Since(start).Seconds()
	if api.tel != nil {
		api.tel.flowOutcomes.Add(ctx, 1, metric.WithAttributes(attrOutcome.String(outcomeFromStatus(sr.statusCode))))
		api.tel.flowDuration.Record(ctx, elapsed)
	}
}

// createThingInstrumented wraps createThing with flow telemetry.
func (api ThingAPI) createThingInstrumented(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("github.com/benc-uk/go-rest-api")
	ctx, span := tracer.Start(r.Context(), "flow.createThing",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	start := time.Now()

	if api.tel != nil {
		api.tel.flowEntries.Add(ctx, 1)
	}

	// validation step span
	_, valSpan := tracer.Start(ctx, "flow.validation.createThing")
	valSpan.SetAttributes(attribute.String("validation.step", "body"))
	valSpan.SetAttributes(attribute.String("outcome", "passed"))
	valSpan.End()

	if api.tel != nil {
		api.tel.flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(attrStep.String("body"), attrOutcome.String("passed")))
	}

	sr := newStatusRecorder(w)
	api.createThing(sr, r.WithContext(ctx))

	elapsed := time.Since(start).Seconds()
	if api.tel != nil {
		api.tel.flowOutcomes.Add(ctx, 1, metric.WithAttributes(attrOutcome.String(outcomeFromStatus(sr.statusCode))))
		api.tel.flowDuration.Record(ctx, elapsed)
	}
}

// deleteThingInstrumented wraps deleteThing with flow telemetry.
func (api ThingAPI) deleteThingInstrumented(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("github.com/benc-uk/go-rest-api")
	ctx, span := tracer.Start(r.Context(), "flow.deleteThing",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()

	start := time.Now()

	if api.tel != nil {
		api.tel.flowEntries.Add(ctx, 1)
	}

	// validation step span
	_, valSpan := tracer.Start(ctx, "flow.validation.deleteThing")
	valSpan.SetAttributes(attribute.String("validation.step", "id-param"))
	valSpan.SetAttributes(attribute.String("outcome", "passed"))
	valSpan.End()

	if api.tel != nil {
		api.tel.flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(attrStep.String("id-param"), attrOutcome.String("passed")))
	}

	sr := newStatusRecorder(w)
	api.deleteThing(sr, r.WithContext(ctx))

	elapsed := time.Since(start).Seconds()
	if api.tel != nil {
		api.tel.flowOutcomes.Add(ctx, 1, metric.WithAttributes(attrOutcome.String(outcomeFromStatus(sr.statusCode))))
		api.tel.flowDuration.Record(ctx, elapsed)
	}
}
