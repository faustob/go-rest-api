// ----------------------------------------------------------------------------
// OpenTelemetry setup & shared instruments for the API.
//
// This package owns the ONE meter and ONE tracer for the service. The SDK is
// built and registered as the global provider by InitProvider, which is called
// from main(). The OTLP endpoint is env driven (OTEL_EXPORTER_OTLP_ENDPOINT).
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope for this service.
const ScopeName = "github.com/benc-uk/go-rest-api"

// P99LatencyBudget is the P99 objective for handlers. Requests slower than this
// get a span event so the tail can be triaged from the trace.
const P99LatencyBudget = 750 * time.Millisecond

// ONE meter and ONE tracer per service - every instrument below is created from
// these. They resolve through the global provider registered by InitProvider.
var (
	meter  = otel.Meter(ScopeName)
	tracer = otel.Tracer(ScopeName)
)

// Shared instruments. Go rebinds instruments created before SDK registration,
// so package level creation is safe here.
var (
	requestsTotal    metric.Int64Counter
	authAttemptTotal metric.Int64Counter
)

func init() {
	var err error

	// Request outcome + throughput counter: availability and request rate are
	// computable from this without scanning traces. Latency itself comes from
	// otelchi's http.server.request.duration histogram.
	requestsTotal, err = meter.Int64Counter(
		"http.server.request.outcome",
		metric.WithDescription("Count of inbound HTTP requests by route, status and outcome class"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create http.server.request.outcome counter: %s", err)
	}

	// Auth decision counter, tagged with the outcome and the denial class.
	authAttemptTotal, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		log.Printf("### 📡 OTel: failed to create auth.attempts counter: %s", err)
	}
}

// InitProvider builds the OTel SDK and registers it as the GLOBAL provider.
// It returns a shutdown func that flushes buffered telemetry.
func InitProvider(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, err
	}

	// Exporters read OTEL_EXPORTER_OTLP_ENDPOINT (and friends) from the env.
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("### 📡 OTel: telemetry initialised for service %s", serviceName)

	return func(shutdownCtx context.Context) error {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			return err
		}

		return mp.Shutdown(shutdownCtx)
	}, nil
}

// statusRecorder captures the response status code. It forwards the optional
// interfaces a http.ResponseWriter may implement so streaming, SSE, WebSocket
// upgrades and the io.Copy fast path keep working.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
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
		if s.status == 0 {
			s.status = http.StatusOK
		}

		return rf.ReadFrom(r)
	}

	return io.Copy(s.ResponseWriter, r)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// errorClass maps a status code to a low cardinality outcome/error class.
func errorClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// RequestOutcomeMiddleware records the request outcome/throughput counter and
// annotates the server span for slow (P99 budget breaching) and 5xx requests.
// It preserves behavior exactly: it only observes what the chain does.
func RequestOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()

		// Always record, even if a downstream handler panics up to Recoverer.
		defer func() {
			elapsed := time.Since(start)

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}

			// Route TEMPLATE, not the raw path - read AFTER the chain has run,
			// as chi only populates the RouteContext during routing.
			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}

			class := errorClass(status)

			attrs := []attribute.KeyValue{
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.HTTPRoute(route),
				semconv.HTTPResponseStatusCode(status),
				attribute.String("http.response.status_class", class),
			}

			if status >= 500 {
				attrs = append(attrs, semconv.ErrorTypeKey.String(class))
			}

			if requestsTotal != nil {
				requestsTotal.Add(r.Context(), 1, metric.WithAttributes(attrs...))
			}

			// Tail latency & error triage: annotate the active server span.
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				if status >= 500 {
					span.SetAttributes(semconv.ErrorTypeKey.String(class))
				}

				if elapsed > P99LatencyBudget {
					span.AddEvent("slow_request", trace.WithAttributes(
						attribute.String("http.route", route),
						attribute.String("slo.budget", P99LatencyBudget.String()),
						attribute.Float64("duration_s", elapsed.Seconds()),
						attribute.String("http.response.status_class", class),
					))
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// RecordAuthAttempt records one authentication/authorization decision.
// scheme is a low cardinality class (never a token, user or message).
func RecordAuthAttempt(ctx context.Context, allowed bool, scheme string) {
	if authAttemptTotal == nil {
		return
	}

	outcome := "denied"
	if allowed {
		outcome = "allowed"
	}

	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
		attribute.String("auth.scheme", scheme),
	}

	if !allowed {
		attrs = append(attrs, semconv.ErrorTypeKey.String("auth_denied"))
	}

	authAttemptTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// Tracer returns the single service tracer for use by other packages.
func Tracer() trace.Tracer {
	return tracer
}
