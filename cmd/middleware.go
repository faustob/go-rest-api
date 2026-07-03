// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// HTTP middleware that wraps every request with otelhttp (emits
// http.server.request.duration with semconv attributes) and tracks
// in-flight request count for the saturation SLI.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const p99BudgetSeconds = 0.750 // 750 ms P99 budget

// otelMiddleware wraps the router with otelhttp (for http.server.request.duration
// and distributed tracing) and adds in-flight request tracking.
func otelMiddleware(next http.Handler) http.Handler {
	// otelhttp emits http.server.request.duration with the correct semconv
	// attributes (http.request.method, http.route, http.response.status_code,
	// url.scheme, network.protocol.version) automatically.
	instrumented := otelhttp.NewMiddleware("go-rest-api")(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tel != nil {
			tel.activeRequests.Add(1)
			defer tel.activeRequests.Add(-1)
		}

		start := time.Now()
		instrumented.ServeHTTP(w, r)
		elapsed := time.Since(start).Seconds()

		// Slow-request span event for P99 triage (SLI: go-rest-api-http-latency-p99)
		if elapsed > p99BudgetSeconds {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow_request",
				trace.WithAttributes(
					attribute.Float64("handler.duration_s", elapsed),
					attribute.String("http.route", r.URL.Path),
				),
			)
		}
	})
}
