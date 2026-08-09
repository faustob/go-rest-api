// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2022
// Licensed under the MIT License.
//
// Middleware available to any API
// ----------------------------------------------------------------------------

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// slowRequestThreshold is the P99 latency budget, requests over it get a span event
const slowRequestThreshold = 750 * time.Millisecond

// Get a value from JWT claim and add it to the request context
// Note: Ignores any errors, such as missing token or claim
func (b *Base) JWTRequestEnricher(fieldName string, claim string) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if len(authHeader) == 0 {
				next.ServeHTTP(w, r)

				return
			}

			authParts := strings.Split(authHeader, " ")
			if len(authParts) != 2 {
				next.ServeHTTP(w, r)

				return
			}

			if strings.ToLower(authParts[0]) != "bearer" {
				next.ServeHTTP(w, r)

				return
			}

			value, err := getClaimFromJWT(authParts[1], claim)
			if err != nil {
				next.ServeHTTP(w, r)

				return
			}

			// nolint:staticcheck
			ctx := context.WithValue(r.Context(), fieldName, value)
			next.ServeHTTP(w, r.WithContext(ctx))
		}

		return http.HandlerFunc(fn)
	}
}

// SimpleCORSMiddleware adds permissive and open CORS policy
func (b *Base) SimpleCORSMiddleware(next http.Handler) http.Handler {
	log.Printf("### 🎭 API: configured simple CORS")

	cors := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
		MaxAge:           300,
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cors.Handler(next).ServeHTTP(w, r)
	})
}

// OTelTelemetryMiddleware enriches the OpenTelemetry server span and the semconv
// http.server.request.duration histogram (both emitted by otelhttp) with the matched
// chi route TEMPLATE, the error class for 5xx responses and a slow request span event.
// It must be registered inside otelhttp.NewMiddleware, see cmd/server.go
func (b *Base) OTelTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// chi's wrapper forwards Flusher/Hijacker/Pusher/ReaderFrom, so SSE & streaming still work
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		elapsed := time.Since(start)

		// Route pattern is only populated once chi has routed the request, so read it here
		route := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		attrs := make([]attribute.KeyValue, 0, 2)
		if route != "" {
			attrs = append(attrs, semconv.HTTPRoute(route))
		}

		if status >= http.StatusInternalServerError {
			attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		}

		// otelhttp reads the labeler after the handler returns, these attributes land
		// on the http.server.request.duration metric
		labeler, _ := otelhttp.LabelerFromContext(r.Context())
		labeler.Add(attrs...)

		span := trace.SpanFromContext(r.Context())
		if span.IsRecording() {
			span.SetAttributes(attrs...)

			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, "")
			}

			if elapsed > slowRequestThreshold {
				span.AddEvent("slow.request", trace.WithAttributes(
					attribute.Float64("http.server.request.duration", elapsed.Seconds()),
					attribute.Int("http.response.status_code", status),
				))
			}
		}
	})
}

// getClaimFromJWT is a helper to return a claim from a JWT
// It decodes the raw JWT, parses the JSON and returns the claim
func getClaimFromJWT(jwtRaw string, claimName string) (string, error) {
	jwtParts := strings.Split(jwtRaw, ".")

	// Decode base64 main part of the token
	tokenBytes, err := base64.RawURLEncoding.DecodeString(jwtParts[1])
	if err != nil {
		log.Println("### Auth: Error in base64 decoding token", err)
		return "", err
	}

	// Parse token JSON
	var tokenJSON map[string]interface{}

	err = json.Unmarshal(tokenBytes, &tokenJSON)
	if err != nil {
		log.Println("### Auth: Error in JSON parsing token", err)
		return "", err
	}

	// Get the claim
	claim, ok := tokenJSON[claimName]
	if !ok {
		log.Println("### Auth: Claim not found in token", err)
		return "", err
	}

	return claim.(string), nil
}
