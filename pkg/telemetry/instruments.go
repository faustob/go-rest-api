// ----------------------------------------------------------------------------
// Single service meter and all metric instruments live here.
// ----------------------------------------------------------------------------

package telemetry

import (
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const scopeName = "github.com/benc-uk/go-rest-api"

// ONE meter per service - every instrument is created from this meter.
var meter = otel.Meter(scopeName)

var (
	// Semconv inbound HTTP server latency histogram, in SECONDS.
	serverDuration metric.Float64Histogram

	// Request outcome counter (availability / error-rate / throughput SLIs).
	requestsTotal metric.Int64Counter

	// Auth decision outcome counter (auth failure rate SLI).
	authAttempts metric.Int64Counter

	// In-flight requests, goes up and down so must be an UpDownCounter.
	requestsActive metric.Int64UpDownCounter
)

func init() {
	var err error

	serverDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP server requests"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create http.server.request.duration: %s", err)
	}

	requestsTotal, err = meter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class (no semconv equivalent; dotted custom name)"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create http.server.request.count: %s", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create auth.attempts: %s", err)
	}

	requestsActive, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create http.server.active_requests: %s", err)
	}
}
