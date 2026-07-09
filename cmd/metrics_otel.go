// ----------------------------------------------------------------------------
// OpenTelemetry metrics: MeterProvider bootstrap plus custom instruments for
// request outcome, auth attempts, worker pool saturation, per-tenant
// throughput, and flow-level business metrics.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"go.opentelemetry.io/otel/attribute"
)

const meterScope = "github.com/benc-uk/go-rest-api"

var (
	// httpRequestOutcome counts requests labeled by route and outcome class.
	httpRequestOutcome metric.Int64Counter
	// authAttempts counts auth decisions labeled by outcome/reason.
	authAttempts metric.Int64Counter
	// flowOutcomes counts terminal outcomes of the primary business flow.
	flowOutcomes metric.Int64Counter
	// flowEntries counts entries into the primary business flow.
	flowEntries metric.Int64Counter
	// flowValidationOutcomes counts validation step pass/fail outcomes.
	flowValidationOutcomes metric.Int64Counter
	// flowDuration records end-to-end flow latency in seconds.
	flowDuration metric.Float64Histogram
	// flowFreshness records entry-to-terminal duration in seconds.
	flowFreshness metric.Float64Histogram

	// activeRequestsGauge and workerPoolSizeGauge are observable gauges for saturation.
	activeRequestsGauge metric.Int64ObservableGauge
	workerPoolSizeGauge metric.Int64ObservableGauge
)

type otelMetricsShutdown func(context.Context) error

// setupOTelMetrics builds an OTLP gRPC MeterProvider, registers it globally,
// and initializes all custom instruments used across the service.
func setupOTelMetrics(ctx context.Context) (otelMetricsShutdown, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	svcName := os.Getenv("OTEL_SERVICE_NAME")
	if svcName == "" {
		svcName = serviceName2()
	}

	metricExporterOpts := []otlpmetricgrpc.Option{}
	if endpoint != "" {
		metricExporterOpts = append(metricExporterOpts, otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, metricExporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(svcName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build OTel resource: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetMeterProvider(mp)

	meter := otel.Meter(meterScope)

	httpRequestOutcome, err = meter.Int64Counter(
		"http.server.request.outcome.total",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create http.server.request.outcome.total counter: %w", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts.total",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth.attempts.total counter: %w", err)
	}

	flowOutcomes, err = meter.Int64Counter(
		"flow.outcomes.total",
		metric.WithDescription("Count of terminal outcomes for the primary business flow"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create flow.outcomes.total counter: %w", err)
	}

	flowEntries, err = meter.Int64Counter(
		"flow.entries.total",
		metric.WithDescription("Count of entries into the primary business flow"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create flow.entries.total counter: %w", err)
	}

	flowValidationOutcomes, err = meter.Int64Counter(
		"flow.validation.outcomes.total",
		metric.WithDescription("Count of per-step validation outcomes for the primary business flow"),
		metric.WithUnit("{outcome}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create flow.validation.outcomes.total counter: %w", err)
	}

	flowDuration, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create flow.duration histogram: %w", err)
	}

	flowFreshness, err = meter.Float64Histogram(
		"flow.entry_to_terminal.duration",
		metric.WithDescription("Wall-clock time between flow entry and terminal state"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create flow.entry_to_terminal.duration histogram: %w", err)
	}

	activeRequestsGauge, err = meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create http.server.active_requests gauge: %w", err)
	}

	workerPoolSizeGauge, err = meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured size of the HTTP worker pool (GOMAXPROCS-based)"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create http.server.worker_pool.size gauge: %w", err)
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, activeRequestCount())
		o.ObserveInt64(workerPoolSizeGauge, int64(maxWorkers()))
		return nil
	}, activeRequestsGauge, workerPoolSizeGauge)
	if err != nil {
		return nil, fmt.Errorf("failed to register saturation gauge callback: %w", err)
	}

	return mp.Shutdown, nil
}

// recordHTTPRequestOutcome records a request outcome data point on the
// http.server.request.outcome.total counter. Called from the outcome-tracking
// middleware registered in server.go.
func recordHTTPRequestOutcome(ctx context.Context, route string, outcome string) {
	if httpRequestOutcome == nil {
		return
	}
	httpRequestOutcome.Add(ctx, 1, metric.WithAttributes(
		semconv.HTTPRoute(route),
		attribute.String("outcome", outcome),
	))
}

// recordAuthAttempt records an auth decision on the auth.attempts.total counter.
// Called from the JWT validator middleware.
func recordAuthAttempt(ctx context.Context, outcome string) {
	if authAttempts == nil {
		return
	}
	authAttempts.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// recordFlowEntry records entry into the primary business flow (thing creation).
func recordFlowEntry(ctx context.Context) {
	if flowEntries == nil {
		return
	}
	flowEntries.Add(ctx, 1)
}

// recordFlowValidationOutcome records a validation step pass/fail outcome.
func recordFlowValidationOutcome(ctx context.Context, outcome string) {
	if flowValidationOutcomes == nil {
		return
	}
	flowValidationOutcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// recordFlowOutcome records the terminal outcome of the primary business flow
// along with its end-to-end duration and entry-to-terminal freshness.
func recordFlowOutcome(ctx context.Context, outcome string, durationSeconds float64) {
	if flowOutcomes != nil {
		flowOutcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
	if flowDuration != nil {
		flowDuration.Record(ctx, durationSeconds, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
	if flowFreshness != nil {
		flowFreshness.Record(ctx, durationSeconds, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}
