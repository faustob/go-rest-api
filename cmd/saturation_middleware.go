// ----------------------------------------------------------------------------
// Saturation middleware — tracks in-flight HTTP requests
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"sync/atomic"
)

// saturationMiddleware increments/decrements the activeRequestCount gauge so
// the http.server.active_requests observable gauge always reflects live load.
func saturationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeRequestCount, 1)
		defer atomic.AddInt64(&activeRequestCount, -1)
		next.ServeHTTP(w, r)
	})
}
