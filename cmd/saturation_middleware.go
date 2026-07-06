// ----------------------------------------------------------------------------
// Saturation middleware — tracks in-flight requests and records flow outcomes.
// Wraps each request to call recordRequestStart / recordRequestEnd so the
// http.server.active_requests gauge and flow.outcomes counter are always updated.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// statusRecorder is a minimal http.ResponseWriter wrapper that captures the
// written status code. It intentionally does NOT wrap optional interfaces
// (Flusher, Hijacker) because otelhttp (registered at the outer layer) already
// handles those; this recorder sits inside the otelhttp handler.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}

// saturationMiddleware tracks in-flight requests and records per-request
// flow outcomes (flow.outcomes, flow.duration, http.server.active_requests).
func saturationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Resolve the matched route template for low-cardinality attributes.
		// chi.RouteContext is populated by the time the middleware runs.
		routePattern := "/"
		if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
			routePattern = rctx.RoutePattern()
		}

		recordRequestStart(r.Context(), routePattern)
		start := time.Now()

		sr := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(sr, r)

		duration := time.Since(start).Seconds()
		recordRequestEnd(r.Context(), routePattern, sr.statusCode, duration)
	})
}
