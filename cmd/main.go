// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Main entry point for the go-rest-api service.
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
	"github.com/go-chi/cors"
)

type ThingAPI struct {
	*API
}

func main() {
	ctx := context.Background()

	// Initialise OpenTelemetry SDK and register global providers.
	shutdown, err := initOTel(ctx)
	if err != nil {
		log.Printf("WARNING: OpenTelemetry init failed, continuing without telemetry: %v", err)
	} else {
		defer func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(shutCtx); err != nil {
				log.Printf("WARNING: OTel shutdown error: %v", err)
			}
		}()
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "go-rest-api"
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.AllowAll().Handler)

	api := &API{}
	thingAPI := ThingAPI{api}

	r.Get("/api/things", thingAPI.getThings)
	r.Get("/api/things/{id}", thingAPI.getThingByID)
	r.Post("/api/things", thingAPI.createThing)
	r.Delete("/api/things/{id}", thingAPI.deleteThing)

	handler := withOTelMiddleware(r, serviceName)

	log.Printf("Starting %s on :%s", serviceName, port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
