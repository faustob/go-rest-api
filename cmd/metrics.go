// ----------------------------------------------------------------------------
// Application-level metric instruments.
// All instruments are created once here and recorded at their call sites.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"
)

// Instrument handles — initialised by initMetrics() after initOTel().
var (
	// http.server.request.duration is emitted by otelhttp middleware automatically.
	// We keep a separate request counter for availability / throughput SLIs.
	httpRequestTotal    metric.Int64Counter
	httpActiveRequests  metric.Int64UpDownCounter

	// auth.attempts counter for authentication failure-rate SLI.
	authAttempts metric.Int64Counter

	// flow.outcomes counter for E2E business-flow success-rate SLI.
	flowOutcomes metric.Int64Counter

	// flow.duration histogram for E2E flow latency / freshness SLIs.
	flowDuration metric.Float64Histogram

	// flow.validation.outcomes counter for validation failure-rate SLI.
	flowValidationOutcomes metric.Int64Counter

	// activeRequestsGauge is the atomic counter backing the observable gauge.
	activeRequestsGauge int64
)

// initMetrics creates all metric instruments against the global meter.
// Must be called after initOTel() so that globalMeter is set.
func initMetrics() error {
	var err error

	httpRequestTotal, err = globalMeter.Int64Counter(
		"http.server.requests.total",
		metric.WithDescription("Total number of HTTP requests received"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	httpActiveRequests, err = globalMeter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of HTTP requests currently in-flight"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	authAttempts, err = globalMeter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	flowOutcomes, err = globalMeter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcomes of the primary E2E business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowDuration, err = globalMeter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	flowValidationOutcomes, err = globalMeter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Outcomes of per-step request validation"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return err
	}

	// Observable gauge for active requests (backed by atomic counter).
	activeReqGauge, err := globalMeter.Int64ObservableGauge(
		"http.server.active_requests.gauge",
		metric.WithDescription("Observable gauge of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}
	_, err = globalMeter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeReqGauge, atomic.LoadInt64(&activeRequestsGauge))
		return nil
	}, activeReqGauge)
	if err != nil {
		return err
	}

	return nil
}
