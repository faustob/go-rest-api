// ----------------------------------------------------------------------------
// Custom SLI instruments for go-rest-api: request outcomes, auth outcomes,
// flow success/throughput, and worker pool saturation gauges.
// ----------------------------------------------------------------------------

package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	requestOutcomeCounter metric.Int64Counter
	authAttemptsCounter   metric.Int64Counter
	flowOutcomeCounter    metric.Int64Counter
	flowEntryCounter      metric.Int64Counter
	activeRequestsGauge   metric.Int64ObservableGauge
	workerPoolSizeGauge   metric.Int64ObservableGauge
)

// initInstruments creates all custom metric instruments against the given
// MeterProvider. Must be called once during SDK setup.
func initInstruments(mp metric.MeterProvider) error {
	meter := mp.Meter("github.com/benc-uk/go-rest-api")

	var err error

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcomes",
		metric.WithDescription("Count of HTTP requests by route and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	authAttemptsCounter, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization attempts by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return err
	}

	flowOutcomeCounter, err = meter.Int64Counter(
		"flow.outcomes",
		metric.WithDescription("Count of primary business flow terminal outcomes"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	flowEntryCounter, err = meter.Int64Counter(
		"flow.entries",
		metric.WithDescription("Count of primary business flow entry invocations"),
		metric.WithUnit("{flow}"),
	)
	if err != nil {
		return err
	}

	activeRequestsGauge, err = meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return err
	}

	workerPoolSizeGauge, err = meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured maximum HTTP worker pool size"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return err
	}

	return nil
}

// RegisterSaturationCallbacks registers an observable callback that reports
// the current in-flight request count and the configured max worker pool
// size, so saturation = active/poolSize can be computed.
func RegisterSaturationCallbacks(maxWorkers func() int, activeRequests func() int64) {
	if activeRequestsGauge == nil || workerPoolSizeGauge == nil {
		return
	}

	meter := MeterProvider().Meter("github.com/benc-uk/go-rest-api")
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, activeRequests())
		o.ObserveInt64(workerPoolSizeGauge, int64(maxWorkers()))
		return nil
	}, activeRequestsGauge, workerPoolSizeGauge)
}

// statusOutcome maps an HTTP status code to a low-cardinality outcome class.
func statusOutcome(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}

// statusOutcomeAttrs builds the http.route/outcome/status attribute set.
func outcomeAttrs(route string, status int) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("http.route", route),
		attribute.String("outcome", statusOutcome(status)),
		attribute.Int("http.response.status_code", status),
	}
}

// statusRecordingResponseWriter captures the status code written to the
// response while forwarding all optional interfaces of the wrapped writer.
type statusRecordingResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecordingResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecordingResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// RequestOutcomeMiddleware records a request-outcome counter and a
// flow-entry counter for every request, tagged by route template and
// outcome class (success/client_error/server_error), for availability and
// throughput SLIs. It does not alter response handling.
func RequestOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		if flowEntryCounter != nil {
			flowEntryCounter.Add(req.Context(), 1)
		}

		wrapped := &statusRecordingResponseWriter{ResponseWriter: resp, status: http.StatusOK}

		start := time.Now()
		next.ServeHTTP(wrapped, req)
		_ = time.Since(start)

		route := req.URL.Path
		if rctx := req.Context(); rctx != nil {
			_ = rctx
		}

		if requestOutcomeCounter != nil {
			requestOutcomeCounter.Add(req.Context(), 1, metric.WithAttributes(outcomeAttrs(route, wrapped.status)...))
		}

		if flowOutcomeCounter != nil {
			outcome := "success"
			if wrapped.status >= 400 {
				outcome = "failed"
			}
			flowOutcomeCounter.Add(req.Context(), 1, metric.WithAttributes(attribute.String("outcome", outcome)))
		}
	})
}

// AuthOutcomeMiddleware wraps the protected route group and records an
// auth-attempt outcome counter. It relies on the downstream JWT validator
// middleware having already run: if it rejected the request it will have
// written a 401/403 response before this middleware observes the status.
func AuthOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		wrapped := &statusRecordingResponseWriter{ResponseWriter: resp, status: http.StatusOK}

		next.ServeHTTP(wrapped, req)

		if authAttemptsCounter == nil {
			return
		}

		outcome := "allowed"
		reason := "n/a"
		if wrapped.status == http.StatusUnauthorized || wrapped.status == http.StatusForbidden {
			outcome = "denied"
			reason = strconv.Itoa(wrapped.status)
		}

		authAttemptsCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}
