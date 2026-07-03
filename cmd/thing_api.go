// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// ThingAPI type, constructor, and OTel instrument wiring
// ----------------------------------------------------------------------------

package main

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/benc-uk/go-rest-api/pkg/api"
)

// ThingAPI extends the base API with OTel instruments
type ThingAPI struct {
	*api.Base
	activeRequests         metric.Int64UpDownCounter
	authAttempts           metric.Int64Counter
	flowOutcomes           metric.Int64Counter
	flowDuration           metric.Float64Histogram
	flowValidationOutcomes metric.Int64Counter
}

// NewThingAPI constructs a ThingAPI backed by the shared api.Base
func NewThingAPI() *ThingAPI {
	return &ThingAPI{
		Base: api.NewBase(serviceName, version, buildInfo, healthy),
	}
}

// SetOTelInstruments stores the pre-created OTel instruments on the API
func (api *ThingAPI) SetOTelInstruments(
	activeRequests metric.Int64UpDownCounter,
	authAttempts metric.Int64Counter,
	flowOutcomes metric.Int64Counter,
	flowDuration metric.Float64Histogram,
	flowValidationOutcomes metric.Int64Counter,
) {
	api.activeRequests = activeRequests
	api.authAttempts = authAttempts
	api.flowOutcomes = flowOutcomes
	api.flowDuration = flowDuration
	api.flowValidationOutcomes = flowValidationOutcomes
}

// RecordFlowEntry increments the active-requests UpDownCounter
func (api *ThingAPI) RecordFlowEntry(ctx context.Context) {
	if api.activeRequests != nil {
		api.activeRequests.Add(ctx, 1)
	}
}

// RecordFlowOutcome decrements the active-requests counter, records flow outcome and duration
func (api *ThingAPI) RecordFlowOutcome(ctx context.Context, outcome string, durationSeconds float64) {
	if api.activeRequests != nil {
		api.activeRequests.Add(ctx, -1)
	}
	if api.flowOutcomes != nil {
		api.flowOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}
	if api.flowDuration != nil {
		api.flowDuration.Record(ctx, durationSeconds, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}
}

// RecordValidationOutcome records a per-step validation result
func (api *ThingAPI) RecordValidationOutcome(ctx context.Context, step string, result string) {
	if api.flowValidationOutcomes != nil {
		api.flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(
			attribute.String("step", step),
			attribute.String("result", result),
		))
	}
}

// RecordAuthAttempt records an authentication attempt with the given outcome
func (api *ThingAPI) RecordAuthAttempt(ctx context.Context, outcome string) {
	if api.authAttempts != nil {
		api.authAttempts.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}
}
