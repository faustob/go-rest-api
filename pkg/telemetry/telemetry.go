// ----------------------------------------------------------------------------
// OpenTelemetry instrumentation for the go-rest-api HTTP server.
//
// This file owns the SINGLE service meter/tracer and every instrument created
// from it. It only USES the globally registered providers (the application
// registers the SDK in main() via telemetry.InitSDK).
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope used for all telemetry from this repo
const ScopeName = "github.com/benc-uk/go-rest-api"

// p99LatencyBudget is the P99 SLO budget; handlers slower than this get a span event
const p99LatencyBudget = 750 * time.Millisecond

// ONE meter & tracer per service; every instrument below is created from these
var (
	meter  = otel.Meter(ScopeName)
	tracer = otel.Tracer(ScopeName)

	requestDuration metric.Float64Histogram
	requestCount    metric.Int64Counter
	authAttempts    metric.Int64Counter
)

func init() {
	var err error

	// Standard OTel semantic convention metric: inbound request duration in SECONDS
	requestDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create http.server.request.duration: %s", err)
	}

	// Request outcome / throughput counter (availability, error-rate & request-rate SLIs)
	requestCount, err = meter.Int64Counter(
		"http.server.request.total",
		metric.WithDescription("Count of inbound HTTP requests by route and outcome class"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create http.server.request.total: %s", err)
	}

	// Authentication / authorization decision counter
	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication decisions by outcome and denial reason"),
	)
	if err != nil {
		log.Printf("### OTel: failed to create auth.attempts: %s", err)
	}
}

// statusRecorder captures the response status code while forwarding the FULL
// http.ResponseWriter contract (Flusher, Hijacker, Pusher, ReaderFrom)
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

func (s *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := s.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}

	return http.ErrNotSupported
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

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// Middleware returns chi/net-http middleware emitting OTel server spans (via the
// otelhttp contrib integration) plus semantic-convention HTTP server metrics.
// The chi route TEMPLATE is only available AFTER the inner handler has run.
func Middleware(serviceName string) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				elapsed := time.Since(start)

				// Route TEMPLATE (low cardinality) - populated only once routing has happened
				route := ""
				if rctx := chi.RouteContext(r.Context()); rctx != nil {
					route = rctx.RoutePattern()
				}

				if route == "" {
					route = "unmatched"
				}

				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}

				attrs := []attribute.KeyValue{
					attribute.String("http.request.method", r.Method),
					attribute.String("url.scheme", scheme),
					attribute.String("http.route", route),
					attribute.Int("http.response.status_code", rec.status),
					attribute.String("network.protocol.version", strconv.Itoa(r.ProtoMajor)+"."+strconv.Itoa(r.ProtoMinor)),
				}

				if rec.status >= 400 {
					// error.type as a low-cardinality status CLASS, never a message
					attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rec.status)))
				}

				set := metric.WithAttributes(attrs...)

				if requestDuration != nil {
					requestDuration.Record(r.Context(), elapsed.Seconds(), set)
				}

				if requestCount != nil {
					requestCount.Add(r.Context(), 1, set)
				}

				span := trace.SpanFromContext(r.Context())
				if span.IsRecording() {
					span.SetAttributes(attrs...)

					if rec.status >= 500 {
						span.SetStatus(codes.Error, "")
					}

					// Slow-request span event for P99 triage
					if elapsed > p99LatencyBudget {
						span.AddEvent("http.slow_request", trace.WithAttributes(
							attribute.String("http.route", route),
							attribute.Float64("http.server.request.duration", elapsed.Seconds()),
							attribute.Float64("slo.budget", p99LatencyBudget.Seconds()),
						))
					}
				}
			}()

			next.ServeHTTP(rec, r)
		})

		// otelhttp provides the server span & context propagation
		return otelhttp.NewHandler(metricsHandler, serviceName)
	}
}

// RecordAuthAttempt records one authentication/authorization decision.
// outcome is "allowed" or "denied"; reason is a low-cardinality denial class.
func RecordAuthAttempt(ctx context.Context, outcome string, reason string) {
	if authAttempts == nil {
		return
	}

	attrs := []attribute.KeyValue{attribute.String("outcome", outcome)}
	if reason != "" {
		attrs = append(attrs, attribute.String("error.type", reason))
	}

	authAttempts.Add(ctx, 1, metric.WithAttributes(attrs...))

	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(attrs...)
	}
}

// Tracer exposes the single service tracer for other packages
func Tracer() trace.Tracer {
	return tracer
}
