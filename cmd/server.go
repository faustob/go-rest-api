// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Sample and example API server, using the go-rest-api package
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"os"
	"regexp"
	"time"

	"github.com/benc-uk/go-rest-api/pkg/auth"
	"github.com/benc-uk/go-rest-api/pkg/env"
	"github.com/benc-uk/go-rest-api/pkg/logging"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	_ "github.com/joho/godotenv/autoload"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

var (
	healthy     = true               // Simple health flag
	version     = "0.0.1"            // App version number, set at build time with -ldflags "-X 'main.version=1.2.3'"
	buildInfo   = "No build details" // Build details, set at build time with -ldflags "-X 'main.buildInfo=Foo bar'"
	serviceName = "change-me"
	defaultPort = 8000
)

func initOTel(ctx context.Context) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	return func(ctx context.Context) error {
		if err := tp.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down tracer provider: %v", err)
		}
		if err := mp.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down meter provider: %v", err)
		}
		return nil
	}, nil
}

func main() {
	// Port to listen on, change the default as you see fit
	serverPort := env.GetEnvInt("PORT", defaultPort)

	// Initialise OpenTelemetry SDK
	ctx := context.Background()
	shutdown, err := initOTel(ctx)
	if err != nil {
		log.Printf("Warning: failed to initialise OTel SDK: %v", err)
	} else {
		defer shutdown(ctx)
	}

	// Core of the REST API
	router := chi.NewRouter()
	api := NewThingAPI()

	// Initialise custom telemetry instruments and wire them to the API
	tel, err := newThingAPITelemetry()
	if err != nil {
		log.Printf("Warning: failed to initialise custom telemetry: %v", err)
	} else {
		api.tel = tel
	}

	// OpenTelemetry HTTP middleware — emits http.server.request.duration histogram
	// and active-request tracking per OTel semantic conventions
	router.Use(otelhttp.NewMiddleware(serviceName))
	router.Use(activeRequestMiddleware)

	// Some basic middleware, change as you see fit, see: https://github.com/go-chi/chi#core-middlewares
	router.Use(middleware.RealIP)
	// Filtered request logger, exclude /metrics & /health endpoints
	router.Use(logging.NewFilteredRequestLogger(regexp.MustCompile(`(^/metrics)|(^/health)`)))
	router.Use(middleware.Recoverer)

	// Some custom middleware for CORS & JWT username
	router.Use(api.SimpleCORSMiddleware)

	// Group of protected routes, this can be all or some of the routes
	router.Group(func(protectedRouter chi.Router) {
		// Fetch the config from the environment, e.g. clientID, JWKS URL, scope etc
		clientID := os.Getenv("AUTH_CLIENT_ID")

		jwtValidator := auth.NewJWTValidator(clientID, "https://change_me/jwks_endpoint", "Some.Scope")

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
