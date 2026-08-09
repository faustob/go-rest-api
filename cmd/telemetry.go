// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation for the API server
//
// Single meter for the service, owning all instruments:
//   - http.server.request.duration (semconv histogram, seconds)
//   - http.server.requests         (request outcome / throughput counter)
//   - http.server.active_requests  (in-flight up-down counter)
//   - auth.attempts                (auth decision outcome counter)
// ----------------------------------------------------------------------------

package main

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"bufio"
	"io"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// p99Budget is the latency budget above which a slow-request span event is added.
const p99Budget = 750 * time.Millisecond

// One meter per service - every instrument is created from this meter.
var meter = otel.Meter("github.com/benc-uk/go-rest-api")

var (
	requestDuration metric.Float64Histogram
	requestsTotal   metric.Int64Counter
	activeRequests  metric.Int64UpDownCounter
	authAttempts    metric.Int64Counter
)

func init() {
	var err error

	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create http.server.request.duration: %v", err)
	}

	requestsTotal, err = meter.Int64Counter(
		"http.server.requests",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create http.server.requests: %v", err)
	}

	activeRequests, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create http.server.active_requests: %v", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Authentication/authorization decisions by outcome"),
	)
	if err != nil {
		log.Printf("### ⚠️ failed to create auth.attempts: %v", err)
	}
}

// statusRecorder captures the response status code while preserving the full
// http.ResponseWriter contract (Flush / Hijack / ReadFrom).
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}

	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}

	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}

	return nil, nil, http.ErrNotSupported
}

func (s *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		if !s.written {
			s.status = http.StatusOK
			s.written = true
		}

		return rf.ReadFrom(r)
	}

	return io.Copy(s.ResponseWriter, r)
}

// telemetryMiddleware creates the server span (via otelhttp) and records the
// semconv request duration histogram plus outcome/throughput counters, using
// the chi route TEMPLATE read AFTER routing has happened.
func telemetryMiddleware(next http.Handler) http.Handler {
	metered := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		// completed is only set true after the handler returns normally; if the
		// handler panics the deferred record uses the "panic" outcome instead of
		// a hardcoded "success".
		completed := false

		baseAttrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", schemeOf(r)),
		}

		if activeRequests != nil {
			activeRequests.Add(r.Context(), 1, metric.WithAttributes(baseAttrs...))

			defer activeRequests.Add(r.Context(), -1, metric.WithAttributes(baseAttrs...))
		}

		defer func() {
			elapsed := time.Since(start)

			// Route pattern is only populated once chi has routed the request
			route := "unmatched"
			if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
				route = rctx.RoutePattern()
			}

			attrs := append([]attribute.KeyValue{}, baseAttrs...)
			attrs = append(attrs,
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
				attribute.String("network.protocol.version", protocolVersion(r)),
				attribute.String("tenant.tier", tenantTier(r)),
			)

			if rec.status >= 500 {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rec.status)))
			}

			outcome := "success"

			switch {
			case !completed:
				outcome = "panic"

				attrs = append(attrs, attribute.String("error.type", "panic"))
			case rec.status >= 500:
				outcome = "server_error"
			case rec.status >= 400:
				outcome = "client_error"
			}

			attrsWithOutcome := append(append([]attribute.KeyValue{}, attrs...),
				attribute.String("http.outcome", outcome))

			if requestDuration != nil {
				requestDuration.Record(r.Context(), elapsed.Seconds(), metric.WithAttributes(attrs...))
			}

			if requestsTotal != nil {
				requestsTotal.Add(r.Context(), 1, metric.WithAttributes(attrsWithOutcome...))
			}

			// Slow-request span event for P99 triage
			if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
				span.SetAttributes(attribute.String("http.route", route))

				if elapsed > p99Budget {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.String("http.route", route),
						attribute.Float64("duration_s", elapsed.Seconds()),
						attribute.Float64("budget_s", p99Budget.Seconds()),
						attribute.Int("http.response.status_code", rec.status),
					))
				}
			}
		}()

		next.ServeHTTP(rec, r)
		completed = true
	})

	// otelhttp creates the server span and handles propagation
	return otelhttp.NewHandler(metered, "http.server")
}

// authTelemetryMiddleware counts every authentication decision. It runs BEFORE
// the JWT validator so that short-circuited denials are still observed.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		// completed is only set true after the downstream chain returns normally.
		completed := false

		defer func() {
			if authAttempts == nil {
				return
			}

			outcome := "allowed"
			reason := "none"

			switch {
			case !completed:
				outcome = "aborted"
				reason = "panic"
			case rec.status == http.StatusUnauthorized:
				outcome = "denied"

				if r.Header.Get("Authorization") == "" {
					reason = "missing_credentials"
				} else {
					reason = "invalid_token"
				}
			case rec.status == http.StatusForbidden:
				outcome = "denied"
				reason = "insufficient_scope"
			}

			route := "unmatched"
			if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
				route = rctx.RoutePattern()
			}

			authAttempts.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("auth.outcome", outcome),
				attribute.String("auth.denial_reason", reason),
				attribute.String("http.route", route),
				attribute.String("http.request.method", r.Method),
			))
		}()

		next.ServeHTTP(rec, r)
		completed = true
	})
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		return "https"
	}

	return "http"
}

func protocolVersion(r *http.Request) string {
	switch r.ProtoMajor {
	case 2:
		return "2"
	case 1:
		if r.ProtoMinor == 0 {
			return "1.0"
		}

		return "1.1"
	default:
		return strconv.Itoa(r.ProtoMajor)
	}
}

// tenantTier is a LOW cardinality business dimension for cohort-aware SLOs.
func tenantTier(r *http.Request) string {
	tier := r.Header.Get("X-Tenant-Tier")

	switch tier {
	case "free", "standard", "premium", "enterprise":
		return tier
	default:
		return "unknown"
	}
}
