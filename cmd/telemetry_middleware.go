// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2020
// Licensed under the MIT License.
//
// Middleware to track in-flight HTTP requests for the
// http.server.active_requests observable gauge, and to record auth outcomes.
// ----------------------------------------------------------------------------

package main

import (
	"net/http"
)

// activeRequestsMiddleware tracks the number of in-flight requests currently
// being served, feeding the http.server.active_requests observable gauge.
func activeRequestsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		incActiveRequests()
		defer decActiveRequests()
		next.ServeHTTP(w, r)
	})
}

// authOutcomeMiddleware records the auth.attempts counter for every request
// that reaches it. It is registered AFTER the JWT validator middleware, so it
// only observes requests that passed authentication/authorization; requests
// rejected by the validator short-circuit before reaching this middleware and
// are not double-counted here.
func authOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordAuthAttempt(r.Context(), "success")
		next.ServeHTTP(w, r)
	})
}
