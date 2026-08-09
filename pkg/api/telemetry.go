// ----------------------------------------------------------------------------
// OpenTelemetry request telemetry for any API built on this package.
//
// This is library code: it NEVER builds or registers an SDK, it only uses the
// globally registered provider (the application binary does the registration).
//
// The semconv http.server.request.duration histogram is emitted by the otelhttp
// middleware; what we add here is the route-level outcome/throughput counter
// (availability, 5xx error rate, per-tenant request rate) and the slow-request
// span event used to triage P99 regressions.
// ----------------------------------------------------------------------------

package api

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99Budget is the P99 latency objective for handlers. Requests slower than
// this get a span event so the tail can be triaged from the trace.
const p99Budget = 750 * time.Millisecond

// Single meter for this service, every instrument below is created from it.
var (
	meter = otel.Meter("github.com/benc-uk/go-rest-api/pkg/api")

	// requestCount is the request outcome counter backing the availability,
	// 5xx error rate and throughput SLIs.
	requestCount, _ = meter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("Inbound HTTP requests by route, status and outcome class"),
		metric.WithUnit("{request}"),
	)

	// activeRequests goes up and down, so it must be an UpDownCounter.
	activeRequests, _ = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
		metric.WithUnit("{request}"),
	)
)

// statusRecorder wraps http.ResponseWriter purely to capture the status code
// that was actually written.
//
// Go's net/http implies 200 when a handler writes a body without ever calling
// WriteHeader, so we default to 200 and latch only the FIRST WriteHeader call.
//
// IMPORTANT: embedding http.ResponseWriter only promotes Header/Write/
// WriteHeader, silently dropping the optional interfaces. This package's own
// pkg/sse/streamer.go performs an unchecked w.(http.Flusher).Flush(), which
// would panic if Flusher were lost, so every optional interface is forwarded
// below with its exact signature.
type statusRecorder struct {
	http.ResponseWriter

	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}

	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	// An implicit 200: body written with no explicit WriteHeader.
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}

	return s.ResponseWriter.Write(b)
}

// Flush forwards to the inner writer, keeping SSE/streaming working.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the inner writer, keeping WebSocket upgrades working.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}

	return nil, nil, http.ErrNotSupported
}

// ReadFrom forwards to the inner writer, keeping the io.Copy/sendfile fast path.
func (s *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}

	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}

	return io.Copy(s.ResponseWriter, r)
}

// Push forwards HTTP/2 server push support.
func (s *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := s.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}

	return http.ErrNotSupported
}

// Unwrap exposes the original writer (http.ResponseController convention).
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// tenantOf returns a low-cardinality tenant/cohort label for a request.
// It deliberately returns a bounded set of values and never an API key or ID.
func tenantOf(r *http.Request) string {
	if tier := r.Header.Get("X-Tenant-Tier"); tier != "" {
		switch tier {
		case "free", "standard", "premium", "enterprise":
			return tier
		}

		return "other"
	}

	return "unknown"
}

// outcomeClass maps a status code to a low-cardinality outcome class.
func outcomeClass(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}

// RequestTelemetryMiddleware records the per-route request outcome counter and
// in-flight gauge, and flags requests that blow the P99 budget with a span
// event. It is transparent: it only calls the next handler and observes it.
//
// It must be registered AFTER middleware.Recoverer and BEFORE any auth
// middleware whose rejections should be counted.
func RequestTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tenant := tenantOf(r)

		if activeRequests != nil {
			activeRequests.Add(ctx, 1, metric.WithAttributes(
				attribute.String("http.request.method", r.Method),
			))
		}

		// Capture the real status code written by the handler chain.
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		defer func() {
			elapsed := time.Since(start)

			if activeRequests != nil {
				activeRequests.Add(ctx, -1, metric.WithAttributes(
					attribute.String("http.request.method", r.Method),
				))
			}

			// chi only populates the route pattern once routing has happened,
			// i.e. AFTER next.ServeHTTP has run. Never label with the raw path.
			route := ""
			if rc := chi.RouteContext(r.Context()); rc != nil {
				route = rc.RoutePattern()
			}

			if route == "" {
				route = "unmatched"
			}

			status := rec.status

			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", status),
				attribute.String("outcome", outcomeClass(status)),
				attribute.String("tenant.tier", tenant),
			}

			if status >= 500 {
				// error.type as a low-cardinality status class, never a message.
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(status)))
			}

			if requestCount != nil {
				requestCount.Add(ctx, 1, metric.WithAttributes(attrs...))
			}

			// Annotate the otelhttp server span for the metrics -> trace pivot.
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				span.SetAttributes(
					attribute.String("tenant.tier", tenant),
					attribute.String("outcome", outcomeClass(status)),
				)

				if status >= 500 {
					span.SetAttributes(attribute.String("error.type", strconv.Itoa(status)))
				}

				if elapsed > p99Budget {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.String("http.route", route),
						attribute.Float64("duration_s", elapsed.Seconds()),
						attribute.Float64("budget_s", p99Budget.Seconds()),
					))
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}
