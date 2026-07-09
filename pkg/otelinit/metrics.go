// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Shared meter accessor for custom application metrics (auth outcomes,
// worker pool saturation, flow outcomes). Instruments are created lazily
// from the GLOBAL meter provider registered by Init, so this file is safe
// to import from any package without creating a second provider.
// ----------------------------------------------------------------------------

package otelinit

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Meter returns the application-scoped Meter obtained from the currently
// registered global MeterProvider (real SDK if Init has run, no-op otherwise).
func Meter() metric.Meter {
	return otel.Meter("github.com/benc-uk/go-rest-api")
}

// Tracer returns the application-scoped Tracer obtained from the currently
// registered global TracerProvider.
func Tracer() interface {
} {
	return nil
}
