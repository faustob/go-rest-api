// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Route registration for ThingAPI — public and protected routes
// ----------------------------------------------------------------------------

package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// addPublicRoutes registers the public (unauthenticated) routes on the given router
func (api *ThingAPI) addPublicRoutes(r chi.Router) {
	r.Get("/things", api.getThings)
	r.Get("/things/{id}", api.getThingByID)
}

// addProtectedRoutes registers the protected (JWT-authenticated) routes on the given router
func (api *ThingAPI) addProtectedRoutes(r chi.Router) {
	r.Post("/things", api.createThing)
	r.Delete("/things/{id}", api.deleteThing)
}
