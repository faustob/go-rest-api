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
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/benc-uk/go-rest-api/pkg/telemetry"
)

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

// OtelMiddleware records the standard OTel HTTP server request duration metric and a
// tracing span for every request, plus the SLI signals for saturation and the primary
// request flow. Must be registered AFTER middleware.Recoverer (so panics are still
// recovered) and BEFORE any auth/validation middleware whose rejections must be counted.
func (b *Base) OtelMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Track in-flight requests for the saturation SLI
		stopInFlight := telemetry.StartInFlight()
		defer stopInFlight()

		ctx, span := telemetry.Tracer().Start(r.Context(), "HTTP "+r.Method)
		defer span.End()

		// Every request is treated as one invocation of the primary business flow
		telemetry.RecordFlowEntry(ctx)

		// Wrap the response writer to capture the status code, while preserving
		// Flusher/Hijacker/ReaderFrom via chi's wrapper (needed for SSE/streaming)
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r.WithContext(ctx))

		duration := time.Since(start)
		statusCode := ww.Status()

		// A handler that never explicitly calls WriteHeader reports 0 from the wrapper,
		// but net/http implicitly sends 200 OK in that case - reflect that here so the
		// duration metric and flow outcome aren't mislabeled for successful requests
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		// Route TEMPLATE, not the raw path - only populated once chi has finished routing
		route := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		// Low cardinality business dimension for cohort-aware latency SLOs
		tenantTier := r.Header.Get("X-Tenant-Tier")
		if tenantTier == "" {
			tenantTier = "standard"
		}

		errType := ""
		flowOutcome := "success"

		if statusCode >= 500 {
			errType = fmt.Sprintf("HTTP%d", statusCode)
			flowOutcome = "failure"
			span.SetStatus(codes.Error, errType)
		}

		span.SetAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.Int("http.response.status_code", statusCode),
			attribute.String("http.route", route),
		)

		// Slow-request span event once the P99 latency budget (750ms) is exceeded
		if duration > 750*time.Millisecond {
			span.AddEvent("slow_request_budget_exceeded", trace.WithAttributes(
				attribute.Float64("duration_seconds", duration.Seconds()),
				attribute.String("http.route", route),
			))
		}

		telemetry.RecordHTTPServerRequest(ctx, r.Method, scheme, route, statusCode, tenantTier, duration, errType)
		telemetry.RecordFlowOutcome(ctx, flowOutcome, duration)
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
