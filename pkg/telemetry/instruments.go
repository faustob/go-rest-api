// ----------------------------------------------------------------------------
// Telemetry instruments: request outcome/latency, auth attempts, saturation
// gauges, and business-flow success/throughput counters. Instruments are
// defined once here and recorded from middleware / route handlers.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/benc-uk/go-rest-api"

var (
	meter = otel.Meter(meterName)

	// http.server.request.duration is emitted by otelhttp itself; we add a
	// low-cardinality outcome-class counter alongside it for direct availability math.
	requestOutcomeCounter metric.Int64Counter

	authAttemptsCounter metric.Int64Counter

	flowOutcomeCounter  metric.Int64Counter
	flowEntryCounter    metric.Int64Counter
	flowDurationHist    metric.Float64Histogram
	validationOutcomeCounter metric.Int64Counter

	activeRequests int64 // in-flight request gauge backing value
	maxWorkers     int64 = 100 // configured worker pool size, adjust as appropriate
)

// InitInstruments creates all metric instruments exactly once. Must be called
// once at startup (from main) before any request is served.
func InitInstruments() error {
	var err error

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP server requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization attempts by outcome and reason"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of primary business flow terminal outcomes"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of primary business flow entry invocations"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowDurationHist, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	validationOutcomeCounter, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Count of per-step validation outcomes within the primary flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return err
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP server worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequests))
		o.ObserveInt64(poolSizeGauge, atomic.LoadInt64(&maxWorkers))
		return nil
	}, activeRequestsGauge, poolSizeGauge)
	if err != nil {
		return err
	}

	return nil
}
