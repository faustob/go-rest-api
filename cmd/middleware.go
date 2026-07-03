// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// HTTP middleware for OpenTelemetry instrumentation.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// workerPoolSize returns the configured worker pool size.
// Uses GOMAXPROCS as a proxy; replace with your actual pool size if applicable.
func workerPoolSize() int64 {
	return int64(runtime.GOMAXPROCS(0))
}

// activeRequestMiddleware tracks in-flight requests for the saturation gauge.
func activeRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequestCount, 1)
		defer atomic.AddInt64(&activeRequestCount, -1)
		next.ServeHTTP(w, r)
	})
}

// p99BudgetMiddleware adds a span event when a handler exceeds the P99 budget
// (750 ms) and records the exception type on the span for 5xx attribution.
const p99BudgetSeconds = 0.750

func p99BudgetMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		elapsed := time.Since(start).Seconds()

		span := trace.SpanFromContext(r.Context())
		if !span.IsRecording() {
			return
		}

		// Record 5xx error type on the span for error-rate attribution.
		if rw.status >= 500 {
			span.SetAttributes(attribute.String("error.type", http.StatusText(rw.status)))
			span.SetStatus(codes.Error, http.StatusText(rw.status))
		}

		// Add a span event when the handler exceeds the P99 budget.
		if elapsed > p99BudgetSeconds {
			span.AddEvent("p99.budget.exceeded",
				trace.WithAttributes(
					attribute.Float64("handler.duration.s", elapsed),
					attribute.Int("http.response.status_code", rw.status),
				),
			)
		}
	})
}

// statusRecorder wraps http.ResponseWriter to capture the written status code.
// It forwards the optional http.Flusher and http.Hijacker interfaces so that
// streaming, SSE, and WebSocket upgrades continue to work.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (interface{}, interface{}, error) {
	type hijacker interface {
		Hijack() (interface{}, interface{}, error)
	}
	if h, ok := r.ResponseWriter.(hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}
