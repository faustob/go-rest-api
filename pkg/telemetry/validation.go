// ----------------------------------------------------------------------------
// Per-step validation span helper for the primary business flow.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var validationTracer = otel.Tracer(meterName)

// RecordValidationStep creates a nested span for a single validation step and
// records its pass/fail outcome, plus increments the validation outcome counter.
func RecordValidationStep(ctx context.Context, step string, valid bool) {
	spanCtx, span := validationTracer.Start(ctx, "flow.validation."+step)
	defer span.End()
	_ = spanCtx

	outcome := "passed"
	if !valid {
		outcome = "failed"
	}

	span.SetAttributes(
		attribute.String("validation.step", step),
		attribute.String("validation.outcome", outcome),
	)

	if validationOutcomeCounter != nil {
		validationOutcomeCounter.Add(ctx, 1,
			attribute.String("validation.step", step),
			attribute.String("outcome", outcome),
		)
	}
}
