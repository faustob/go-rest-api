// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Define a simple 'Things' API for the service
// ----------------------------------------------------------------------------

package main

import (
	"github.com/benc-uk/go-rest-api/pkg/api"
	"github.com/go-chi/chi/v5"
)

// ThingAPI is a wrap of the common base API with local implementation
type ThingAPI struct {
	tel *thingAPITelemetry
	*api.Base
	// Add extra fields here: database connections, SDK clients
}

func (api ThingAPI) addPublicRoutes(r chi.Router) {
	r.Get("/things", api.getThingsInstrumented)
	r.Get("/things/{id}", api.getThingByIDInstrumented)
	r.Post("/things", api.createThingInstrumented)
}

func (api ThingAPI) addProtectedRoutes(r chi.Router) {
	// Put methods here that should be protected & need JWT auth, e.g. POST, PUT, DELETE
	r.Delete("/things/{id}", api.deleteThingInstrumented)
}

func NewThingAPI() ThingAPI {
	return ThingAPI{
		Base: api.NewBase(serviceName, version, buildInfo, healthy),
		// Database connections, SDK clients, etc can be added here
	}
}
