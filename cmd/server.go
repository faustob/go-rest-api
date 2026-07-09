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

	"net/http"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	otelchi "go.opentelemetry.io/contrib/instrumentation/github.com/go-chi/chi/v5/otelchi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	_ "github.com/joho/godotenv/autoload"
)

var (
	requestOutcomeCounter metric.Int64Counter
	authOutcomeCounter    metric.Int64Counter
)

// statusRecorder wraps http.ResponseWriter to capture the response status
// code, forwarding optional interfaces the underlying writer may implement.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requestOutcomeMiddleware records a total request-outcome counter, keyed by
// low-cardinality method + outcome (success/error) attributes.
func requestOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		if requestOutcomeCounter != nil {
			outcome := "success"
			if rec.status >= 400 {
				outcome = "error"
			}
			requestOutcomeCounter.Add(r.Context(), 1,
				metric.WithAttributes(
					attribute.String("http.request.method", r.Method),
					attribute.String("outcome", outcome),
				),
			)
		}
	})
}

// authOutcomeMiddleware records an authentication outcome counter for routes
// protected by JWT auth, using the response status set by upstream handlers.
func authOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		if authOutcomeCounter != nil {
			outcome := "success"
			if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
				outcome = "error"
			}
			authOutcomeCounter.Add(r.Context(), 1,
				metric.WithAttributes(
					attribute.String("outcome", outcome),
				),
			)
		}
	})
}

var (
	healthy     = true               // Simple health flag
	version     = "0.0.1"            // App version number, set at build time with -ldflags "-X 'main.version=1.2.3'"
	buildInfo   = "No build details" // Build details, set at build time with -ldflags "-X 'main.buildInfo=Foo bar'"
	serviceName = "change-me"
	defaultPort = 8000
)

func main() {
	// Set up OpenTelemetry SDK (tracer provider, meter provider), registers globally.
	ctx := context.Background()
	otelShutdown, err := setupOTelSDK(ctx)
	if err != nil {
		log.Printf("### ⚠️  Failed to set up OpenTelemetry SDK: %v", err)
	}
	defer func() {
		if otelShutdown != nil {
			_ = otelShutdown(context.Background())
		}
	}()

	meter := otel.Meter(serviceName)

	requestOutcomeCounter, err = meter.Int64Counter(
		"http.server.request.outcome.total",
		metric.WithDescription("Total count of HTTP requests by outcome"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create request outcome counter: %v", err)
	}

	authOutcomeCounter, err = meter.Int64Counter(
		"auth.request.outcome.total",
		metric.WithDescription("Total count of authenticated requests by outcome"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("### ⚠️  Failed to create auth outcome counter: %v", err)
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

	// OpenTelemetry instrumentation for HTTP server spans + metrics
	router.Use(otelchi.Middleware(serviceName, otelchi.WithChiRoutes(router)))

	// Some custom middleware for CORS & JWT username
	router.Use(api.SimpleCORSMiddleware)

	// Request outcome + saturation instrumentation for SLIs
	router.Use(requestOutcomeMiddleware)

	// Group of protected routes, this can be all or some of the routes
	router.Group(func(protectedRouter chi.Router) {
		// Fetch the config from the environment, e.g. clientID, JWKS URL, scope etc
		clientID := os.Getenv("AUTH_CLIENT_ID")

		jwtValidator := auth.NewJWTValidator(clientID, "https://change_me/jwks_endpoint", "Some.Scope")

		protectedRouter.Use(jwtValidator.Middleware)
		protectedRouter.Use(authOutcomeMiddleware)

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
