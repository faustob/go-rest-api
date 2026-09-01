// ----------------------------------------------------------------------------
// HTTP middleware that records OpenTelemetry metrics/spans for the SLIs:
// availability (outcome counter), latency p95/p99 (duration histogram + slow
// request span events), error rate (status/error.type attributes), request
// throughput (per-tenant counter), and auth failure rate (auth outcome
// counter).
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// statusRecorder wraps http.ResponseWriter to capture the response status
// code, while forwarding every optional interface the underlying writer may
// implement (Flusher, Hijacker, ReaderFrom) so streaming/SSE/websocket
// upgrades keep working unmodified.
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

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// pushStatusRecorder additionally implements http.Pusher by delegating to the
// underlying writer's Pusher implementation, so wrapping never drops HTTP/2
// push support when the original writer supported it.
type pushStatusRecorder struct {
	*statusRecorder
	pusher http.Pusher
}

func (r *pushStatusRecorder) Push(target string, opts *http.PushOptions) error {
	return r.pusher.Push(target, opts)
}

// newStatusRecorder wraps w in a statusRecorder, returning a value that also
// implements http.Pusher (via pushStatusRecorder) IF the underlying writer
// supports HTTP/2 push, so that a downstream w.(http.Pusher) assertion still
// succeeds. If w does not implement http.Pusher, a plain *statusRecorder is
// returned.
func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func wrapStatusRecorder(w http.ResponseWriter) (http.ResponseWriter, *statusRecorder) {
	rec := newStatusRecorder(w)
	if pusher, ok := w.(http.Pusher); ok {
		return &pushStatusRecorder{statusRecorder: rec, pusher: pusher}, rec
	}
	return rec, rec
}

// slowRequestBudget is the P99 latency budget; requests exceeding it get a
// span event recorded for triage.
const slowRequestBudget = 750 * time.Millisecond

// HTTPMetricsMiddleware records the standard http.server.request.duration
// histogram plus a request-outcome counter and a per-tenant request counter
// for every request. It reads the matched chi route pattern AFTER
// next.ServeHTTP returns, since routing hasn't populated the RouteContext
// beforehand.
func HTTPMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped, rec := wrapStatusRecorder(w)

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start)

		routePattern := "unknown"
		if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
			routePattern = rctx.RoutePattern()
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		baseAttrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.HTTPRouteKey.String(routePattern),
			semconv.HTTPResponseStatusCodeKey.Int(rec.status),
			attribute.String("url.scheme", scheme),
		}

		if rec.status >= 500 {
			baseAttrs = append(baseAttrs, attribute.String("error.type", "server_error"))
		}

		if RequestDurationHist != nil {
			RequestDurationHist.Record(r.Context(), duration.Seconds(), metric.WithAttributes(baseAttrs...))
		}

		outcome := "success"
		if rec.status >= 500 {
			outcome = "failure"
		}
		if RequestOutcomeCounter != nil {
			outcomeAttrs := append(append([]attribute.KeyValue{}, baseAttrs...), attribute.String("outcome", outcome))
			RequestOutcomeCounter.Add(r.Context(), 1, metric.WithAttributes(outcomeAttrs...))
		}

		tenant := r.Header.Get("X-API-Key")
		if tenant == "" {
			tenant = "unknown"
		}
		if TenantRequestCounter != nil {
			tenantAttrs := append(append([]attribute.KeyValue{}, baseAttrs...), attribute.String("tenant", tenant))
			TenantRequestCounter.Add(r.Context(), 1, metric.WithAttributes(tenantAttrs...))
		}

		if duration >= slowRequestBudget {
			span := trace.SpanFromContext(r.Context())
			span.AddEvent("slow_request_p99_budget_exceeded", trace.WithAttributes(
				semconv.HTTPRouteKey.String(routePattern),
				attribute.Float64("duration_seconds", duration.Seconds()),
			))
		}
	})
}

// AuthOutcomeMiddleware must be registered so it WRAPS the JWT validator
// (i.e. installed BEFORE it in the middleware chain) so that requests the
// validator denies are still observed: it inspects the response status code
// after the whole downstream chain (validator + handler) has run.
func AuthOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped, rec := wrapStatusRecorder(w)

		next.ServeHTTP(wrapped, r)

		outcome := "allowed"
		reason := "none"
		switch {
		case rec.status == http.StatusUnauthorized:
			outcome = "denied"
			reason = "unauthorized"
		case rec.status == http.StatusForbidden:
			outcome = "denied"
			reason = "forbidden"
		}

		if AuthAttemptsCounter != nil {
			AuthAttemptsCounter.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("outcome", outcome),
				attribute.String("reason", reason),
			))
		}
	})
}
