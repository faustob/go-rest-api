// ----------------------------------------------------------------------------
// HTTP middleware that wraps every request with:
//   - otelhttp span + http.server.request.duration histogram (OTel semconv)
//   - active-request UpDownCounter
//   - flow entry counter, flow outcome counter, flow duration histogram
//   - flow validation outcome counter (JWT auth check)
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const p99BudgetSeconds = 0.750 // 750 ms P99 budget

// slowRequestHandler wraps next and emits a slow_request span event when the
// handler exceeds the P99 budget. It must be installed as the handler passed
// INTO otelhttp.NewHandler so that it runs inside the otelhttp span, where
// trace.SpanFromContext returns the live server span.
func slowRequestHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		elapsed := time.Since(start).Seconds()
		if elapsed > p99BudgetSeconds {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow_request",
				trace.WithAttributes(
					attribute.Float64("handler.duration_s", elapsed),
					attribute.String("http.request.method", r.Method),
				),
			)
		}
	})
}

// withOTelMiddleware wraps the root mux with otelhttp (which emits
// http.server.request.duration) and layers our custom business-metric
// middleware on top.
func withOTelMiddleware(next http.Handler, serviceName string) http.Handler {
	// otelhttp emits http.server.request.duration with OTel semconv attributes.
	// slowRequestHandler is installed inside otelhttp so span.AddEvent runs
	// within the live server span.
	otelHandler := otelhttp.NewHandler(slowRequestHandler(next), serviceName,
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inst := globalInstruments
		if inst == nil {
			// SDK not initialised — serve without instrumentation to preserve behavior.
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()

		// --- flow.entry: count every inbound request ---
		inst.flowEntry.Add(ctx, 1,
			attribute.String("http.request.method", r.Method),
		)

		// --- active requests: increment on entry, decrement on exit ---
		inst.activeRequests.Add(ctx, 1)
		defer inst.activeRequests.Add(ctx, -1)

		// Wrap ResponseWriter to capture status code.
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		start := time.Now()

		// Delegate to otelhttp (which creates the server span and records
		// http.server.request.duration). The inner handler captures the
		// otelhttp-created span from its own context and emits the slow_request
		// event there, where the span is live.
		otelHandler.ServeHTTP(rw, r.WithContext(ctx))

		elapsed := time.Since(start).Seconds()
		statusCode := rw.status
		method := r.Method
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		// Determine outcome class.
		outcome := "success"
		if statusCode >= 500 {
			outcome = "error"
		} else if statusCode >= 400 {
			outcome = "client_error"
		}

		baseAttrs := []attribute.KeyValue{
			attribute.String("http.request.method", method),
			attribute.String("url.scheme", scheme),
			attribute.Int("http.response.status_code", statusCode),
			attribute.String("outcome", outcome),
		}

		// --- flow.outcomes: terminal outcome of the primary flow ---
		inst.flowOutcomes.Add(ctx, 1, baseAttrs...)

		// --- flow.duration: end-to-end flow duration histogram ---
		inst.flowDuration.Record(ctx, elapsed, baseAttrs...)

		// --- flow.validation.outcomes: treat 401/403 as validation failures ---
		validationOutcome := "passed"
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			validationOutcome = "failed"
			// Also record auth.attempts as denied.
			inst.authAttempts.Add(ctx, 1,
				attribute.String("outcome", "denied"),
				attribute.String("http.request.method", method),
				attribute.Int("http.response.status_code", statusCode),
				attribute.String("reason", "http_"+strconv.Itoa(statusCode)),
			)
		} else {
			// Successful auth path.
			inst.authAttempts.Add(ctx, 1,
				attribute.String("outcome", "allowed"),
				attribute.String("http.request.method", method),
				attribute.Int("http.response.status_code", statusCode),
			)
		}
		inst.flowValidationOutcomes.Add(ctx, 1,
			attribute.String("outcome", validationOutcome),
			attribute.String("http.request.method", method),
		)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the written status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}
