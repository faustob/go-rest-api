// ----------------------------------------------------------------------------
// Application-level telemetry instruments.
// All instruments are defined here and recorded at their measurement sites.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const meterScope = "github.com/benc-uk/go-rest-api"

// appMetrics holds all custom application-level instruments.
type appMetrics struct {
	// auth.attempts — counts every JWT auth decision (outcome: allowed/denied, reason)
	authAttempts metric.Int64Counter
	// flow.outcomes — counts completed request flows (outcome: success/failure)
	flowOutcomes metric.Int64Counter
	// flow.duration — histogram of end-to-end flow wall-clock time in seconds
	flowDuration metric.Float64Histogram
	// flow.entries — counts every flow entry invocation
	flowEntries metric.Int64Counter
	// flow.validation.outcomes — counts per-step validation results
	flowValidationOutcomes metric.Int64Counter
	// http.server.active_requests — in-flight request gauge (atomic)
	activeRequestsGauge int64 // updated atomically; observed via callback
}

// globalMetrics is the singleton used by middleware and handlers.
var globalMetrics *appMetrics

// initAppMetrics creates all instruments against the global MeterProvider.
// Must be called AFTER initOTel so the provider is registered.
func initAppMetrics() {
	m := otel.Meter(meterScope)
	am := &appMetrics{}

	var err error

	am.authAttempts, err = m.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total JWT authentication/authorisation decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("otel: auth.attempts counter: %v", err)
	}

	am.flowOutcomes, err = m.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of end-to-end request flows"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		log.Printf("otel: flow.outcomes counter: %v", err)
	}

	am.flowDuration, err = m.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end request flow wall-clock duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("otel: flow.duration histogram: %v", err)
	}

	am.flowEntries, err = m.Int64Counter(
		"flow.entries",
		metric.WithDescription("Number of times the primary flow entry point was invoked"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		log.Printf("otel: flow.entries counter: %v", err)
	}

	am.flowValidationOutcomes, err = m.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation outcomes (pass/fail)"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		log.Printf("otel: flow.validation.outcomes counter: %v", err)
	}

	// Observable gauge for in-flight requests (saturation SLI).
	activeReqGauge, err := m.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of HTTP requests currently being handled"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("otel: http.server.active_requests gauge: %v", err)
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeReqGauge, atomic.LoadInt64(&am.activeRequestsGauge))
		return nil
	}, activeReqGauge)
	if err != nil {
		log.Printf("otel: active_requests callback: %v", err)
	}

	globalMetrics = am
}
