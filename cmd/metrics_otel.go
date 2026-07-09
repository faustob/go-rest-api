// ----------------------------------------------------------------------------
// Custom OpenTelemetry metrics for the go-rest-api service: authentication
// outcome counters and per-tenant request counters, recorded from the JWT
// middleware and route handlers respectively.
// ----------------------------------------------------------------------------

package main

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	appMeter                metric.Meter
	authAttemptsCounter     metric.Int64Counter
	requestsByTenantCounter metric.Int64Counter
	customMetricsOnce       sync.Once
)

// initCustomMetrics obtains the meter and creates the custom instruments.
// It must be called AFTER the global MeterProvider has been registered
// (i.e. after initOTelSDK runs in main()), otherwise otel.Meter would bind
// to the no-op provider. sync.Once guards against re-initialization.
func initCustomMetrics() {
	customMetricsOnce.Do(func() {
		appMeter = otel.Meter("github.com/benc-uk/go-rest-api")

		var err error

		// authAttemptsCounter counts every authentication decision, tagged by outcome
		// (allowed/denied) and reason, supporting the auth-failure-rate SLI.
		authAttemptsCounter, err = appMeter.Int64Counter(
			"auth.attempts.total",
			metric.WithDescription("Count of authentication attempts by outcome"),
			metric.WithUnit("{attempt}"),
		)
		if err != nil {
			panic(err)
		}

		// requestsByTenantCounter counts requests broken out by tenant/client id,
		// supporting per-tenant throughput SLIs.
		requestsByTenantCounter, err = appMeter.Int64Counter(
			"http.server.requests_by_tenant.total",
			metric.WithDescription("Count of HTTP requests by tenant/client id"),
			metric.WithUnit("{request}"),
		)
		if err != nil {
			panic(err)
		}
	})
}
