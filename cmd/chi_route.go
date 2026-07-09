// ----------------------------------------------------------------------------
// Helper to extract the matched chi route template from a request, used to
// keep the http.route attribute low-cardinality.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// chiRouteContext returns the matched route pattern (e.g. "/things/{id}")
// for the given request if chi has already matched a route, or "" otherwise.
func chiRouteContext(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return ""
	}

	pattern := rctx.RoutePattern()
	if pattern == "" {
		return ""
	}

	return pattern
}
