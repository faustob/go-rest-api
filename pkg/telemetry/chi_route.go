// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Helper to extract the matched chi route pattern (low-cardinality route template)
// ----------------------------------------------------------------------------

package telemetry

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type chiRouteContext interface {
	RoutePattern() string
}

func chiRouteCtx(r *http.Request) chiRouteContext {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return nil
	}
	return rctx
}
