// ----------------------------------------------------------------------------
// Single meter/tracer for the service plus all metric instruments.
// One meter per service: every instrument below is created from `meter`.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const scopeName = "github.com/benc-uk/go-rest-api"

var (
	meter = otel.Meter(scopeName)

	// Semconv inbound request duration histogram, in SECONDS.
	requestDuration metric.Float64Histogram

	// Request outcome / throughput counter (availability + error rate + throughput).
	requestOutcome metric.Int64Counter

	// In-flight requests (goes up and down -> UpDownCounter).
	requestsInFlight metric.Int64UpDownCounter

	// Auth decision outcome counter.
	authAttempts metric.Int64Counter

	// Business-level handler outcome counter.
	handlerOutcome metric.Int64Counter
)

func init() {
	var err error

	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create http.server.request.duration: %v", err)
	}

	requestOutcome, err = meter.Int64Counter(
		"api.http.server.requests",
		metric.WithDescription("Inbound HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create api.http.server.requests: %v", err)
	}

	requestsInFlight, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create http.server.active_requests: %v", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create auth.attempts: %v", err)
	}

	handlerOutcome, err = meter.Int64Counter(
		"api.handler.outcome",
		metric.WithDescription("Business outcome of API handlers"),
		metric.WithUnit("{operation}"),
	)
	if err != nil {
		log.Printf("### ⚠️ otel: failed to create api.handler.outcome: %v", err)
	}
}

// RecordHandlerOutcome records a business-level outcome for a named handler.
func RecordHandlerOutcome(ctx context.Context, handlerName, outcome string) {
	if handlerOutcome == nil {
		return
	}

	handlerOutcome.Add(ctx, 1, metric.WithAttributes(
		attribute.String("api.handler", handlerName),
		attribute.String("api.outcome", outcome),
	))
}
