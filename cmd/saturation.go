// ----------------------------------------------------------------------------
// In-flight request tracking used to populate the http.server.active_requests
// and http.server.worker_pool.size observable gauges (saturation SLI).
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"runtime"
	"sync/atomic"
)

var inFlightRequests int64

// activeRequestCount returns the current number of in-flight HTTP requests.
func activeRequestCount() int64 {
	return atomic.LoadInt64(&inFlightRequests)
}

// maxWorkers returns the configured worker pool size proxy (GOMAXPROCS).
func maxWorkers() int {
	return runtime.GOMAXPROCS(0)
}

// saturationTrackingMiddleware increments/decrements the in-flight request
// counter around each request, used by the active_requests gauge callback.
func saturationTrackingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&inFlightRequests, 1)
		defer atomic.AddInt64(&inFlightRequests, -1)
		next.ServeHTTP(w, r)
	})
}
