// ----------------------------------------------------------------------------
// main.go — application entry point.
// Bootstraps OTel SDK, registers metrics, and wires otelhttp + telemetry
// middleware around the Chi router.
// ----------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	ctx := context.Background()

	// Bootstrap OTel SDK (TracerProvider + MeterProvider).
	shutdown, err := initOTel(ctx)
	if err != nil {
		log.Fatalf("failed to initialise OpenTelemetry: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdown(shutCtx); err != nil {
			log.Printf("OTel shutdown error: %v", err)
		}
	}()

	// Create metric instruments.
	if err := initMetrics(); err != nil {
		log.Fatalf("failed to initialise metrics: %v", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	api := ThingAPI{}

	r.Get("/things", api.getThings)
	r.Get("/things/{id}", api.getThingByID)
	r.Post("/things", api.createThing)
	r.Delete("/things/{id}", api.deleteThing)

	// Wrap the entire router with:
	//   1. otelhttp — emits http.server.request.duration histogram (latency SLIs)
	//      with correct semconv attributes (method, route, status_code).
	//   2. telemetryMiddleware — records availability, saturation, flow, and
	//      validation counters / histograms.
	handler := otelhttp.NewHandler(
		telemetryMiddleware(r),
		"go-rest-api",
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	)

	addr := ":8080"
	if v := os.Getenv("PORT"); v != "" {
		addr = ":" + v
	}
	log.Printf("Listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// ThingAPI is the receiver type for route handlers.
type ThingAPI struct{}
