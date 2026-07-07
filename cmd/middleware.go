// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Custom middleware for the ThingAPI — active-request tracking and
// slow-request span events for the P99 latency SLI.
// ----------------------------------------------------------------------------

package main

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// activeRequestMiddleware increments/decrements the in-flight request gauge
// and adds a span event when the handler exceeds the P99 latency budget.
func activeRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequestCount, 1)
		start := time.Now()

		defer func() {
			atomic.AddInt64(&activeRequestCount, -1)
			elapsed := time.Since(start).Seconds()

			if elapsed > p99BudgetSeconds {
				span := trace.SpanFromContext(r.Context())
				span.AddEvent("slow.request", trace.WithAttributes(
					attribute.String("http.route", r.URL.Path),
					attribute.String("http.request.method", r.Method),
					attribute.String("slow.request.duration", fmt.Sprintf("%.3fs", elapsed)),
				))
			}
		}()

		next.ServeHTTP(w, r)
	})
}
