// ----------------------------------------------------------------------------
// HTTP server telemetry middleware: request outcome counter, per-tenant
// throughput counter, auth-failure counter, saturation gauges, and
// slow-request span events for P99 triage.
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const meterName = "github.com/benc-uk/go-rest-api"

var meter = otel.Meter(meterName)

var (
	requestOutcomeCounter metric.Int64Counter
	requestRateCounter    metric.Int64Counter
	authAttemptsCounter   metric.Int64Counter
	flowOutcomeCounter    metric.Int64Counter
	flowEntryCounter      metric.Int64Counter
	activeRequests        atomic.Int64

	// p99 budget used to decide when to emit a slow-request span event.
	p99Budget = 750 * time.Millisecond
	// worker pool size is a static configuration value for this service.
	maxWorkers int64 = 100
)

// initInstruments creates every instrument from the single package meter.
// Must be called once, after the global MeterProvider has been registered.
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

	requestRateCounter, err = meter.Int64Counter(
		"http.server.request.total",
		metric.WithDescription("Total HTTP requests, broken out by tenant"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Terminal outcome count of the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of entries into the primary request flow"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	poolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured maximum worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, activeRequests.Load())
		o.ObserveInt64(poolSizeGauge, maxWorkers)
		return nil
	}, activeRequestsGauge, poolSizeGauge)
	if err != nil {
		return err
	}

	return nil
}

// statusRecorder wraps http.ResponseWriter to capture the status code while
// preserving the optional interfaces (Flush, Hijack, ReadFrom) the original
// writer may implement.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack delegates to the underlying ResponseWriter's Hijacker interface if
// implemented, preserving support for protocol upgrades (e.g. WebSockets).
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support Hijack")
}

// ReadFrom delegates to the underlying ResponseWriter's io.ReaderFrom
// interface if implemented, preserving sendfile-style optimizations.
func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// RequestTelemetryMiddleware records request outcome, throughput, flow, and
// saturation telemetry, plus emits a slow-request span event when the P99
// latency budget is exceeded. Must be registered after middleware.Recoverer
// and before auth-related middleware so auth denials are still observed by
// downstream handlers/route matching, and before route-specific auth groups.
func RequestTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeRequests.Add(1)
		defer activeRequests.Add(-1)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		tenant := r.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}

		flowEntryCounter.Add(r.Context(), 1)

		next.ServeHTTP(rec, r)

		duration := time.Since(start)

		// Route template must be read AFTER ServeHTTP, once chi has populated
		// the RouteContext.
		route := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}
		if route == "" {
			route = "unmatched"
		}

		outcome := "success"
		if rec.status >= 500 {
			outcome = "server_error"
		} else if rec.status >= 400 {
			outcome = "client_error"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
			attribute.Int("http.response.status_code", rec.status),
		}

		requestOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(attrs...))
		requestRateCounter.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("tenant", tenant),
		))

		flowStatus := "success"
		if rec.status >= 400 {
			flowStatus = "failure"
		}
		flowOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", flowStatus),
		))

		// Slow-request span event for P99 triage.
		if duration > p99Budget {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow_request", trace.WithAttributes(
				attribute.String("http.route", route),
				attribute.Int64("duration_ms", duration.Milliseconds()),
				attribute.Int64("p99_budget_ms", p99Budget.Milliseconds()),
			))
		}

		if rec.status >= 500 {
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(ErrorTypeAttr("server_error"))
		}
	})
}

// RecordAuthOutcome records an authentication/authorization decision. It is
// intended to be called from the JWT validator middleware for every request
// it processes, tagged with the outcome ("allowed"/"denied") and, on denial,
// the reason class.
func RecordAuthOutcome(ctx context.Context, outcome string, reason string) {
	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
	}
	if reason != "" {
		attrs = append(attrs, attribute.String("reason", reason))
	}
	authAttemptsCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}
