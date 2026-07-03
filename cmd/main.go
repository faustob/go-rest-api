// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Main entry point — initialises OTel SDK then starts the HTTP server.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/benc-uk/go-rest-api/pkg/api"
	"github.com/benc-uk/go-rest-api/pkg/auth"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ThingAPI is the main API struct
type ThingAPI struct {
	api.Base
	auth.JWT
}

func main() {
	// Load .env file if present
	_ = godotenv.Load()

	// --- Initialise OpenTelemetry SDK ---
	ctx := context.Background()
	shutdown, err := initOTel(ctx)
	if err != nil {
		log.Printf("Warning: OTel init failed: %v", err)
	} else {
		defer func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(shutCtx); err != nil {
				log.Printf("OTel shutdown error: %v", err)
			}
		}()
	}

	// Determine port
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	// Build the ThingAPI
	thingAPI := ThingAPI{}
	thingAPI.Base.Init("Things", "v1", port)

	// JWT auth (optional — only if JWKS_URI is set)
	jwksURI := os.Getenv("JWKS_URI")
	if jwksURI != "" {
		if err := thingAPI.JWT.Init(jwksURI); err != nil {
			log.Fatalf("JWT init failed: %v", err)
		}
	}

	// Router
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.AllowAll().Handler)

	// --- OTel HTTP middleware ---
	// otelhttp.NewMiddleware emits http.server.request.duration (semconv)
	// with http.request.method, http.route, http.response.status_code, url.scheme.
	r.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewMiddleware("go-rest-api")(next)
	})
	r.Use(activeRequestMiddleware)
	r.Use(p99BudgetMiddleware)

	// Routes
	r.Get("/api/things", withFlowInstrumentation(thingAPI.getThings, "/api/things"))
	r.Get("/api/things/{id}", withFlowInstrumentation(thingAPI.getThingByID, "/api/things/{id}"))
	r.Post("/api/things", withFlowInstrumentation(thingAPI.createThing, "/api/things"))
	r.Delete("/api/things/{id}", withFlowInstrumentation(thingAPI.deleteThing, "/api/things/{id}"))

	log.Printf("Starting server on port %s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// withFlowInstrumentation wraps a handler to emit flow-level telemetry:
// flow.entries counter, flow.outcomes counter, flow.duration histogram,
// and flow.validation.outcomes counter (validation = auth check).
func withFlowInstrumentation(h http.HandlerFunc, route string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Flow entry counter
		flowEntryCounter.Add(r.Context(), 1,
			metricAttr("http.route", route),
		)

		// Validation outcome: treat presence of Authorization header as the
		// validation step; record pass/fail on the current span.
		span := trace.SpanFromContext(r.Context())
		hasAuth := r.Header.Get("Authorization") != ""
		validationOutcome := "passed"
		if !hasAuth && os.Getenv("JWKS_URI") != "" {
			// Auth is required but header is absent — record as failed validation.
			validationOutcome = "failed"
		}
		span.SetAttributes(
			attribute.String("flow.validation.outcome", validationOutcome),
			attribute.String("http.route", route),
		)
		flowValidationOutcomesCounter.Add(r.Context(), 1,
			metricAttr("outcome", validationOutcome),
			metricAttr("http.route", route),
		)

		// Delegate to the real handler.
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rw, r)

		// Flow outcome
		duration := time.Since(start).Seconds()
		outcome := "success"
		if rw.status >= 500 {
			outcome = "failure"
		}
		flowOutcomesCounter.Add(r.Context(), 1,
			metricAttr("outcome", outcome),
			metricAttr("http.route", route),
		)
		flowDurationHistogram.Record(r.Context(), duration,
			metricAttr("http.route", route),
			metricAttr("outcome", outcome),
		)
	}
}
