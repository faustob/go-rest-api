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
	"github.com/benc-uk/go-rest-api/pkg/telemetry"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	_ "github.com/joho/godotenv/autoload"
)

var (
	healthy     = true               // Simple health flag
	version     = "0.0.1"            // App version number, set at build time with -ldflags "-X 'main.version=1.2.3'"
	buildInfo   = "No build details" // Build details, set at build time with -ldflags "-X 'main.buildInfo=Foo bar'"
	serviceName = "change-me"
	defaultPort = 8000
)

func main() {
	// Start OpenTelemetry: builds and registers the global tracer & meter providers.
	// Endpoint is env-driven via OTEL_EXPORTER_OTLP_ENDPOINT.
	otelShutdown, otelErr := initOTel(context.Background(), serviceName, version)
	if otelErr != nil {
		log.Printf("### \U0001F4E1 OTel: telemetry disabled, init failed: %s", otelErr)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := otelShutdown(ctx); err != nil {
				log.Printf("### \U0001F4E1 OTel: shutdown error: %s", err)
			}
		}()
	}

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

	// OpenTelemetry HTTP server metrics & spans. Registered AFTER Recoverer (so panics are still
	// recovered downstream) and BEFORE the JWT validator, so auth rejections are counted too.
	router.Use(telemetry.Middleware)

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
