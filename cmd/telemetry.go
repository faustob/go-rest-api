// ----------------------------------------------------------------------------
// Telemetry helpers — business-level metrics for SLI instrumentation.
// Instruments are defined here and recorded at their measurement sites.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	// P99 budget in seconds — requests exceeding this get a span event.
	p99BudgetSeconds = 0.750
	// Scope name used for all meters / tracers in this service.
	instrumentationScope = "github.com/benc-uk/go-rest-api"
)

// sliMetrics holds every OTel instrument used for SLI recording.
type sliMetrics struct {
	// http.server.request.duration is emitted by otelhttp middleware automatically;
	// we keep a reference here only for the active-requests / pool-size gauges.
	activeRequests metric.Int64ObservableGauge
	workerPoolSize metric.Int64ObservableGauge

	// auth.attempts — counts every JWT validation decision.
	authAttempts metric.Int64Counter

	// flow.outcomes — terminal outcome of the primary request flow.
	flowOutcomes metric.Int64Counter

	// flow.duration — end-to-end wall-clock time of the primary flow.
	flowDuration metric.Float64Histogram

	// flow.entry — incremented at the flow entry point regardless of outcome.
	flowEntry metric.Int64Counter

	// flow.validation.outcomes — per-step validation pass/fail.
	flowValidationOutcomes metric.Int64Counter

	// in-flight request counter (atomic, read by the observable gauge callback).
	inFlightRequests atomic.Int64
}

var sli *sliMetrics

// initSLIMetrics creates all SLI instruments and registers observable callbacks.
// Must be called AFTER initOTel() so the global MeterProvider is set.
func initSLIMetrics() error {
	m := otel.Meter(instrumentationScope)
	s := &sliMetrics{}

	var err error

	s.activeRequests, err = m.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return fmt.Errorf("create http.server.active_requests: %w", err)
	}

	s.workerPoolSize, err = m.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured HTTP worker pool size (GOMAXPROCS)"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return fmt.Errorf("create http.server.worker_pool.size: %w", err)
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(s.activeRequests, s.inFlightRequests.Load())
		// GOMAXPROCS is the closest proxy for the goroutine worker pool size.
		maxProcs := int64(runtimeGOMAXPROCS())
		o.ObserveInt64(s.workerPoolSize, maxProcs)
		return nil
	}, s.activeRequests, s.workerPoolSize)
	if err != nil {
		return fmt.Errorf("register active_requests/pool_size callback: %w", err)
	}

	s.authAttempts, err = m.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Total JWT authentication/authorisation decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return fmt.Errorf("create auth.attempts: %w", err)
	}

	s.flowOutcomes, err = m.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome of the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return fmt.Errorf("create flow.outcomes: %w", err)
	}

	s.flowDuration, err = m.Float64Histogram(
		"flow.duration",
		metric.WithDescription("End-to-end wall-clock duration of the primary request flow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("create flow.duration: %w", err)
	}

	s.flowEntry, err = m.Int64Counter(
		"flow.entry",
		metric.WithDescription("Number of times the primary flow entry point was invoked"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return fmt.Errorf("create flow.entry: %w", err)
	}

	s.flowValidationOutcomes, err = m.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Per-step validation pass/fail outcomes"),
		metric.WithUnit("{validation}"),
	)
	if err != nil {
		return fmt.Errorf("create flow.validation.outcomes: %w", err)
	}

	sli = s
	return nil
}

// sliMiddleware wraps every chi handler to:
//   - track in-flight requests (saturation gauge)
//   - record flow entry, flow outcome, flow duration
//   - add a span event when the handler exceeds the P99 budget
//   - record auth attempt outcomes (reads the X-Auth-Outcome header set by the JWT middleware)
func sliMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sli == nil {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		start := time.Now()

		// Saturation: count in-flight requests.
		sli.inFlightRequests.Add(1)
		defer sli.inFlightRequests.Add(-1)

		// Flow entry counter — incremented unconditionally.
		sli.flowEntry.Add(ctx, 1)

		// Capture response status via a thin wrapper.
		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(ww, r)

		duration := time.Since(start).Seconds()
		statusCode := ww.status

		// Determine outcome class.
		outcome := "success"
		if statusCode >= 500 {
			outcome = "error"
		} else if statusCode >= 400 {
			outcome = "client_error"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.route", r.URL.Path),
			attribute.String("outcome", outcome),
		}

		// Flow outcome counter.
		sli.flowOutcomes.Add(ctx, 1, metric.WithAttributes(attrs...))

		// Flow duration histogram.
		sli.flowDuration.Record(ctx, duration, metric.WithAttributes(
			attribute.String("http.route", r.URL.Path),
		))

		// Slow-request span event for P99 triage.
		if duration > p99BudgetSeconds {
			span := trace.SpanFromContext(ctx)
			span.AddEvent("slow_request",
				trace.WithAttributes(
					attribute.Float64("handler.duration_s", duration),
					attribute.Int("http.response.status_code", statusCode),
					attribute.String("http.route", r.URL.Path),
				),
			)
		}

		// Auth attempt outcome — the JWT middleware sets X-Auth-Outcome.
		if authOutcome := ww.Header().Get("X-Auth-Outcome"); authOutcome != "" {
			authReason := ww.Header().Get("X-Auth-Deny-Reason")
			sli.authAttempts.Add(ctx, 1, metric.WithAttributes(
				attribute.String("outcome", authOutcome),
				attribute.String("deny.reason", authReason),
			))
		}
	})
}

// statusWriter is a minimal http.ResponseWriter wrapper that captures the
// written status code. It forwards Flush so chi's middleware chain works.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (sw *statusWriter) WriteHeader(code int) {
	if !sw.wrote {
		sw.status = code
		sw.wrote = true
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if !sw.wrote {
		sw.wrote = true
	}
	return sw.ResponseWriter.Write(b)
}

func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// runtimeGOMAXPROCS returns the current GOMAXPROCS value.
func runtimeGOMAXPROCS() int {
	return runtimeMaxProcs()
}
