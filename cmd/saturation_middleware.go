// ----------------------------------------------------------------------------
// Saturation middleware — tracks in-flight HTTP requests via an atomic counter
// so the observable gauge in active_requests.go always reflects live concurrency.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
	"sync/atomic"
)

// saturationMiddleware increments activeReqCount when a request arrives and
// decrements it when the handler returns, giving a live in-flight gauge.
func saturationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&activeReqCount, 1)
		defer atomic.AddInt64(&activeReqCount, -1)
		next.ServeHTTP(w, r)
	})
}
