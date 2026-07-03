// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Small helpers shared across the instrumentation files.
// ----------------------------------------------------------------------------

package main

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metricAttr is a convenience wrapper that converts a key/value pair into an
// OTel metric.MeasurementOption (attribute set) for use with counter.Add /
// histogram.Record calls.
func metricAttr(key, value string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(key, value))
}
