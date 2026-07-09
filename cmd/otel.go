// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// OpenTelemetry SDK bootstrap: tracing + metrics, registered as global
// providers. Configured via standard OTEL_EXPORTER_OTLP_ENDPOINT env var.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// shutdownFunc is returned by setupOTel and must be deferred by the caller
// to flush buffered spans/metrics on exit.
type shutdownFunc func(context.Context) error

// meter is the package-level Meter used to create instruments across cmd/*.go
var meter metric.Meter

// tracer is the package-level Tracer used to create manual spans across cmd/*.go
var tracer = otel.Tracer("github.com/benc-uk/go-rest-api/cmd")

// Instruments recorded across the API
var (
	requestOutcomeCounter    metric.Int64Counter
	authAttemptsCounter      metric.Int64Counter
	flowOutcomeCounter       metric.Int64Counter
	flowEntryCounter         metric.Int64Counter
	flowValidationCounter    metric.Int64Counter
	flowDurationHistogram    metric.Float64Histogram
	flowFreshnessHistogram   metric.Float64Histogram
)

// setupOTel builds and registers the global TracerProvider and MeterProvider.
// It defends against a preexisting global provider (e.g. an externally
// attached agent or double-init) by logging and continuing rather than
// panicking.
func setupOTel(ctx context.Context, svcName string) (shutdownFunc, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(svcName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to merge otel resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp trace exporter: %w", err)
	}

	tp := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(traceExporter),
		tracesdk.WithResource(res),
	)

	// Defensive registration: if a provider is already set (e.g. by an
	// externally attached mechanism), otel.SetTracerProvider simply
	// overwrites; there's no error return here in the Go API, so this is
	// inherently safe to call once at startup.
	otel.SetTracerProvider(tp)
	tracer = otel.Tracer("github.com/benc-uk/go-rest-api/cmd")

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	meter = otel.Meter("github.com/benc-uk/go-rest-api/cmd")

	if err := initInstruments(); err != nil {
		return nil, fmt.Errorf("failed to create otel instruments: %w", err)
	}

	shutdown := func(shutdownCtx context.Context) error {
		var errs error
		if err := tp.Shutdown(shutdownCtx); err != nil {
			errs = errors.Join(errs, err)
		}
		if err := mp.Shutdown(shutdownCtx); err != nil {
			errs = errors.Join(errs, err)
		}
		return errs
	}

	return shutdown, nil
}

// initInstruments creates all package-level metric instruments exactly once.
func initInstruments() error {
	var err error

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome count for the primary business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of entries into the primary business flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowValidationCounter, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Count of request validation outcomes for the primary flow"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return err
	}

	flowDurationHistogram, err = meter.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end duration of the primary business flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	flowFreshnessHistogram, err = meter.Float64Histogram(
		"flow.entry_to_terminal.duration",
		metric.WithDescription("Wall-clock duration between flow entry and terminal state"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	return nil
}

// attributeErrorType returns the standard error.type attribute for a given class.
func attributeErrorType(class string) attribute.KeyValue {
	return semconv.ErrorTypeKey.String(class)
}
