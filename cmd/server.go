// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Sample and example API server, using the go-rest-api package
// ----------------------------------------------------------------------------

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/benc-uk/go-rest-api/pkg/auth"
	"github.com/benc-uk/go-rest-api/pkg/env"
	"github.com/benc-uk/go-rest-api/pkg/logging"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	_ "github.com/joho/godotenv/autoload"
)

var (
	healthy     = true               // Simple health flag
	version     = "0.0.1"            // App version number, set at build time with -ldflags "-X 'main.version=1.2.3'"
	buildInfo   = "No build details" // Build details, set at build time with -ldflags "-X 'main.buildInfo=Foo bar'"
	serviceName = "change-me"
	defaultPort = 8000
)

// --- OpenTelemetry instrumentation -----------------------------------------
// One meter/tracer per service, shared by every instrument and span in this
// package.
var (
	meter  = otel.Meter("go-rest-api")
	tracer = otel.Tracer("go-rest-api")

	httpServerDuration  metric.Float64Histogram
	authAttempts        metric.Int64Counter
	validationOutcomes  metric.Int64Counter
	flowRequestCount    metric.Int64Counter
	flowRequestOutcome  metric.Int64Counter
	activeRequestsCount int64
)

func init() {
	var err error

	httpServerDuration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Fatalf("failed to create http.server.request.duration histogram: %v", err)
	}

	authAttempts, err = meter.Int64Counter(
		"auth.attempts",
		metric.WithDescription("Count of authentication/authorization decisions by outcome"),
	)
	if err != nil {
		log.Fatalf("failed to create auth.attempts counter: %v", err)
	}

	validationOutcomes, err = meter.Int64Counter(
		"flow.validation.outcomes",
		metric.WithDescription("Count of request validation steps by outcome"),
	)
	if err != nil {
		log.Fatalf("failed to create flow.validation.outcomes counter: %v", err)
	}

	flowRequestCount, err = meter.Int64Counter(
		"flow.request.count",
		metric.WithDescription("Count of entries into the primary request flow"),
	)
	if err != nil {
		log.Fatalf("failed to create flow.request.count counter: %v", err)
	}

	flowRequestOutcome, err = meter.Int64Counter(
		"flow.request.outcome",
		metric.WithDescription("Count of terminal outcomes of the primary request flow"),
	)
	if err != nil {
		log.Fatalf("failed to create flow.request.outcome counter: %v", err)
	}

	activeRequestsGauge, err := meter.Int64ObservableGauge(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
	)
	if err != nil {
		log.Fatalf("failed to create http.server.active_requests gauge: %v", err)
	}

	workerPoolSizeGauge, err := meter.Int64ObservableGauge(
		"http.server.worker_pool.size",
		metric.WithDescription("Configured size of the HTTP worker pool (GOMAXPROCS)"),
	)
	if err != nil {
		log.Fatalf("failed to create http.server.worker_pool.size gauge: %v", err)
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(activeRequestsGauge, atomic.LoadInt64(&activeRequestsCount))
		o.ObserveInt64(workerPoolSizeGauge, int64(runtime.GOMAXPROCS(0)))
		return nil
	}, activeRequestsGauge, workerPoolSizeGauge)
	if err != nil {
		log.Fatalf("failed to register saturation callback: %v", err)
	}
}

// setupOTel builds and registers the global TracerProvider and MeterProvider.
// The OTLP endpoint is env-driven via OTEL_EXPORTER_OTLP_ENDPOINT.
func setupOTel(ctx context.Context, svcName string) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(svcName),
		),
	)
	if err != nil {
		return nil, err
	}

	// The OTLP endpoint is always sourced from OTEL_EXPORTER_OTLP_ENDPOINT
	// (falling back to the OTel SDK/collector default for local dev only).
	otlpEndpoint := env.GetEnvString("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	if otlpEndpoint == "" {
		return nil, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT must be set")
	}

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(otlpEndpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	return func(shutdownCtx context.Context) error {
		if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return meterProvider.Shutdown(shutdownCtx)
	}, nil
}

func schemeOf(req *http.Request) string {
	if req.TLS != nil {
		return "https"
	}
	return "http"
}

// statusCapturingWriter wraps http.ResponseWriter to capture the response
// status code, forwarding the optional Flusher/Hijacker/ReaderFrom interfaces
// so streaming, SSE and connection upgrades keep working.
type statusCapturingWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusCapturingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
}

func (w *statusCapturingWriter) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}

// httpTelemetryMiddleware records the standard http.server.request.duration
// histogram, active-request saturation, slow-request span events (P99
// triage) and the primary-flow entry/outcome counters for every request.
func httpTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		start := time.Now()

		ctx, span := tracer.Start(req.Context(), "HTTP "+req.Method)
		defer span.End()

		flowRequestCount.Add(ctx, 1, metric.WithAttributes(attribute.String("flow.name", "primary")))

		atomic.AddInt64(&activeRequestsCount, 1)
		defer atomic.AddInt64(&activeRequestsCount, -1)

		sw := &statusCapturingWriter{ResponseWriter: resp, statusCode: http.StatusOK}

		next.ServeHTTP(sw, req.WithContext(ctx))

		duration := time.Since(start)

		route := chi.RouteContext(req.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(req.Method),
			semconv.URLScheme(schemeOf(req)),
			semconv.HTTPRoute(route),
			semconv.HTTPResponseStatusCode(sw.statusCode),
		}

		outcome := "success"
		if sw.statusCode >= 500 {
			outcome = "failure"
			errType := fmt.Sprintf("HTTP_%d", sw.statusCode)
			attrs = append(attrs, semconv.ErrorTypeKey.String(errType))
			span.SetAttributes(semconv.ErrorTypeKey.String(errType))
			span.SetStatus(codes.Error, errType)
		}

		span.SetAttributes(attrs...)
		httpServerDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))

		flowRequestOutcome.Add(ctx, 1, metric.WithAttributes(
			attribute.String("flow.name", "primary"),
			attribute.String("outcome", outcome),
		))

		// P99 budget for this service is 750ms - flag slow requests with a
		// span event so the breakdown can be inspected during triage.
		if duration > 750*time.Millisecond {
			span.AddEvent("slow.request", trace.WithAttributes(
				attribute.String("http.route", route),
				attribute.Float64("duration.seconds", duration.Seconds()),
			))
		}
	})
}

// authTelemetryMiddleware must be registered BEFORE the JWT validator so it
// observes rejected (401/403) requests too.
func authTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		sw := &statusCapturingWriter{ResponseWriter: resp, statusCode: http.StatusOK}

		next.ServeHTTP(sw, req)

		outcome := "allowed"
		reason := "none"
		if sw.statusCode == http.StatusUnauthorized || sw.statusCode == http.StatusForbidden {
			outcome = "denied"
			reason = fmt.Sprintf("HTTP_%d", sw.statusCode)
		}

		authAttempts.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}

// recordValidationOutcome tags the current span and increments the
// flow.validation.outcomes counter for a single validation step.
func recordValidationOutcome(req *http.Request, step string, outcome string) {
	span := trace.SpanFromContext(req.Context())
	span.SetAttributes(
		attribute.String("step", step),
		attribute.String("outcome", outcome),
	)
	span.AddEvent("validation."+outcome, trace.WithAttributes(attribute.String("step", step)))

	validationOutcomes.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("step", step),
		attribute.String("outcome", outcome),
	))
}

func main() {
	ctx := context.Background()

	shutdownOTel, err := setupOTel(ctx, serviceName)
	if err != nil {
		log.Fatalf("failed to set up OpenTelemetry: %v", err)
	}
	defer func() {
		if err := shutdownOTel(ctx); err != nil {
			log.Printf("error shutting down OpenTelemetry: %v", err)
		}
	}()

	// Port to listen on, change the default as you see fit
	serverPort := env.GetEnvInt("PORT", defaultPort)

	// Core of the REST API
	router := chi.NewRouter()
	api := NewThingAPI()

	// Some basic middleware, change as you see fit, see: https://github.com/go-chi/chi#core-middlewares
	router.Use(middleware.RealIP)
	// Filtered request logger, exclude /metrics & /health endpoints
	router.Use(logging.NewFilteredRequestLogger(regexp.MustCompile(`(^/metrics)|(^/health)`)))
	router.Use(middleware.Recoverer)

	// OpenTelemetry: request duration, active-request saturation, slow-request
	// span events and primary-flow entry/outcome counters for every request.
	router.Use(httpTelemetryMiddleware)

	// Some custom middleware for CORS & JWT username
	router.Use(api.SimpleCORSMiddleware)

	// Group of protected routes, this can be all or some of the routes
	router.Group(func(protectedRouter chi.Router) {
		// Fetch the config from the environment, e.g. clientID, JWKS URL, scope etc
		clientID := os.Getenv("AUTH_CLIENT_ID")

		jwtValidator := auth.NewJWTValidator(clientID, "https://change_me/jwks_endpoint", "Some.Scope")

		// Registered before the JWT validator so denied (401/403) requests
		// are also observed for the auth failure-rate SLI.
		protectedRouter.Use(authTelemetryMiddleware)
		protectedRouter.Use(jwtValidator.Middleware)

		// These routes do create, update, delete operations
		api.addProtectedRoutes(protectedRouter)
	})

	// Group of anonymous public routes
	router.Group(func(publicRouter chi.Router) {
		// Add Prometheus metrics endpoint, must be before the other routes
		api.AddMetricsEndpoint(publicRouter, "metrics")

		// Add optional root, health & status endpoints
		api.AddHealthEndpoint(publicRouter, "health", func() bool {
			// Put some better logic here with a real API
			return true
		})
		api.AddStatusEndpoint(publicRouter, "status")
		api.AddOKEndpoint(publicRouter, "")

		// Rest of the app routes are public and don't need JWT auth
		api.addPublicRoutes(publicRouter)
	})

	// *OPTIONAL* Add support for single page applications (SPA) with client-side routing
	//log.Printf("### 🌏 Serving static files for SPA from: %s", "./")
	//router.Handle("/*", static.SpaHandler{
	//	StaticPath: "./static",
	//	IndexFile:  "index.html",
	//})

	// Start the API server, this function will block until the server is stopped
	api.StartServer(serverPort, router, 10*time.Second)
}
