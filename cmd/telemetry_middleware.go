// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Custom telemetry middleware layered on top of otelchi/otelhttp semantics:
// records the request-outcome counter (availability SLI), the primary-flow
// entry/outcome/duration/freshness instruments, and slow-request span events
// for P99 triage.
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99BudgetSeconds is the P99 latency budget (750ms) used to decide whether
// to emit a slow-request span event for triage.
const p99BudgetSeconds = 0.750

// statusCapturingWriter records the status code written to the response
// while forwarding all optional interfaces the underlying ResponseWriter
// may implement, so streaming/SSE/websocket behavior is preserved.
type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusCapturingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter's Hijacker implementation
// so websocket/hijack upgrades continue to work through this wrapper.
func (w *statusCapturingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

// ReadFrom forwards to the underlying ResponseWriter's io.ReaderFrom
// implementation (used for sendfile-style optimizations), falling back to a
// generic copy if not implemented.
func (w *statusCapturingWriter) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		return rf.ReadFrom(r)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return io.Copy(w.ResponseWriter, r)
}

// TelemetryOutcomeMiddleware records the request-outcome counter and the
// primary business flow entry/outcome/duration instruments for every
// request. It relies on otelchi (already installed) for the standard
// http.server.request.duration histogram, and adds business-flow rollups
// plus slow-request triage span events on top.
func (api ThingAPI) TelemetryOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		if flowEntryCounter != nil {
			flowEntryCounter.Add(r.Context(), 1)
		}

		sw := &statusCapturingWriter{ResponseWriter: w}

		next.ServeHTTP(sw, r)

		duration := time.Since(start).Seconds()

		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}

		routeTemplate := chi.RouteContext(r.Context()).RoutePattern()
		if routeTemplate == "" {
			routeTemplate = "unknown"
		}

		outcomeClass := "success"
		if status >= 500 {
			outcomeClass = "server_error"
		} else if status >= 400 {
			outcomeClass = "client_error"
		}

		if requestOutcomeCounter != nil {
			requestOutcomeCounter.Add(r.Context(), 1,
				metric.WithAttributes(
					attribute.String("http.route", routeTemplate),
					attribute.String("outcome", outcomeClass),
					attribute.Int("http.response.status_code", status),
				),
			)
		}

		flowOutcome := "success"
		if status >= 400 {
			flowOutcome = "failed"
		}
		if flowOutcomeCounter != nil {
			flowOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("outcome", flowOutcome),
			))
		}
		if flowDurationHistogram != nil {
			flowDurationHistogram.Record(r.Context(), duration, metric.WithAttributes(
				attribute.String("outcome", flowOutcome),
			))
		}
		if flowFreshnessHistogram != nil {
			flowFreshnessHistogram.Record(r.Context(), duration, metric.WithAttributes(
				attribute.String("http.route", routeTemplate),
			))
		}

		// Slow-request span event for P99 triage
		if duration > p99BudgetSeconds {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.String("http.route", routeTemplate),
				attribute.Float64("handler.duration_seconds", duration),
				attribute.String("http.response.status_code", strconv.Itoa(status)),
			))
		}

		if status >= 500 {
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(attributeErrorType("server_error"))
		}
	})
}


