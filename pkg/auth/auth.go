// --------------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2022
// Licensed under the MIT License.
//
// JWTValidator middleware & wrapper for securing routes with OAuth2 and JWT auth
// --------------------------------------------------------------------------------

package auth

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/benc-uk/go-rest-api/pkg/telemetry"
	"github.com/golang-jwt/jwt/v5"
)

// JWTValidator is a struct that can be used to protect routes
type JWTValidator struct {
	clientID string
	scope    string
	jwks     *keyfunc.JWKS
}

type PassthroughValidator struct {
}

type Validator interface {
	Middleware(next http.Handler) http.Handler
	Protect(next http.HandlerFunc) http.HandlerFunc
}

// NewJWTValidator creates a new JWTValidator struct
func NewJWTValidator(clientID string, jwksURL string, scope string) JWTValidator {
	// Create and store the JWKS once, this will be refreshed automatically
	jwks, err := keyfunc.Get(jwksURL, keyfunc.Options{
		RefreshInterval: time.Duration(1) * time.Hour,
	})

	if err != nil {
		log.Printf("### 🔐 Auth: Failed to fetch the JWKS. Error: %s", err)
	} else {
		log.Printf("### 🔐 Auth: Enabling auth, JWKS fetched from %s", jwksURL)
	}

	return JWTValidator{
		clientID: clientID,
		scope:    scope,
		jwks:     jwks,
	}
}

func NewPassthroughValidator() PassthroughValidator {
	return PassthroughValidator{}
}

// Middleware returns middleware to enforce JWT auth on all routes
func (v JWTValidator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validateRequest(r, v.clientID, v.scope, v.jwks) {
			telemetry.RecordAuthAttempt(r.Context(), false, authDenialReason(r, v.jwks))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		telemetry.RecordAuthAttempt(r.Context(), true, "")

		next.ServeHTTP(w, r)
	})
}

// Protect can be added around any route handler to enforce JWT auth
func (v JWTValidator) Protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validateRequest(r, v.clientID, v.scope, v.jwks) {
			telemetry.RecordAuthAttempt(r.Context(), false, authDenialReason(r, v.jwks))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		telemetry.RecordAuthAttempt(r.Context(), true, "")

		next.ServeHTTP(w, r)
	}
}

// PassthroughValidator middleware does nothing :)
func (v PassthroughValidator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

// PassthroughValidator protect function does nothing :)
func (v PassthroughValidator) Protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	}
}

// authDenialReason classifies WHY a request was denied, for the auth outcome
// counter. It returns a small, fixed set of low-cardinality reason codes and
// never any token content or user identity.
func authDenialReason(r *http.Request, jwks *keyfunc.JWKS) string {
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) == 0 {
		return "missing_credentials"
	}

	authParts := strings.Split(authHeader, " ")
	if len(authParts) != 2 {
		return "malformed_credentials"
	}

	if strings.ToLower(authParts[0]) != "bearer" {
		return "unsupported_scheme"
	}

	// JWKS unavailable is the key signal for the "investigate JWKS first" runbook
	if jwks == nil {
		return "jwks_unavailable"
	}

	return "invalid_token"
}

// validateRequest is an internal function to validate a request
func validateRequest(r *http.Request, clientID string, scope string, jwks *keyfunc.JWKS) bool {
	// Get auth header & bearer scheme
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) == 0 {
		return false
	}

	// Split header into scheme & B64 token	string
	authParts := strings.Split(authHeader, " ")
	if len(authParts) != 2 {
		return false
	}

	if strings.ToLower(authParts[0]) != "bearer" {
		return false
	}

	// JWKS might not have been fetched or some other error with it, if not then deny access
	if jwks == nil {
		log.Printf("### 🔐 Auth: No JWKS, cannot validate token, denying access")
		return false
	}

	// Parse the JWT string using the key fetched from the JWKS
	token, err := jwt.Parse(authParts[1], jwks.Keyfunc)
	if err != nil {
		log.Printf("### 🔐 Auth: Failed to parse the JWT. Error: %s", err)
		return false
	}

	claims := token.Claims.(jwt.MapClaims)

	// Check the scope includes the app scope
	if !strings.Contains(claims["scp"].(string), scope) {
		log.Printf("### 🔐 Auth: Scope '%s' is missing from token scope '%s'", scope, claims["scp"])
		return false
	}

	// Azure AD returns the audience with a prefix of api:// so we need to remove it
	audience := claims["aud"]
	if strings.HasPrefix(audience.(string), "api://") {
		audience = strings.TrimPrefix(audience.(string), "api://")
	}

	// Check the token audience is the client id, this might have already been done by jwt.Parse
	if audience != clientID {
		log.Printf("### 🔐 Auth: Token audience '%s' does not match '%s'", claims["aud"], clientID)
		return false
	}

	return true
}
