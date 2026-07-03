package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	// --- Bootstrap OpenTelemetry SDK (must be first) ---
	ctx := context.Background()
	shutdown, err := initOTel(ctx)
	if err != nil {
		log.Printf("WARNING: OTel init failed: %v — telemetry will be no-op", err)
	} else {
		defer func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(shutCtx); err != nil {
				log.Printf("OTel shutdown error: %v", err)
			}
		}()
	}

	if err := initMetrics(); err != nil {
		log.Printf("WARNING: metric instrument init failed: %v", err)
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "go-rest-api"
	}
	fmt.Fprintf(os.Stderr, "OTel SDK initialised for service %q\n", serviceName)

	// Build the chi router.
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(activeRequestMiddleware)

	api := ThingAPI{}
	r.Get("/things", api.getThings)
	r.Get("/things/{id}", api.getThingByID)
	r.Post("/things", api.createThing)
	r.Delete("/things/{id}", api.deleteThing)

	// Wrap the router with otelhttp so http.server.request.duration (semconv) is emitted.
	handler := otelhttp.NewHandler(r, serviceName)

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Printf("Starting server on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
