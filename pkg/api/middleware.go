// ----------------------------------------------------------------------------
// Copyright (c) Ben Coleman, 2022
// Licensed under the MIT License.
//
// Middleware available to any API
// ----------------------------------------------------------------------------

package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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

// slowRequestThresholdSeconds is the P99 latency budget; requests slower than
// this get a span event so the trace can be used for triage.
const slowRequestThresholdSeconds = 0.75

// RequestTelemetryMiddleware records the OpenTelemetry http.server.request.duration
// histogram plus a request-outcome counter for every request, backing the HTTP
// availability, latency (P95/P99) and throughput SLIs. It also annotates the
// active span (started by otelchi) with error/slow-request details.
func (b *Base) RequestTelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		// Low-cardinality business dimension, used to cohort latency/throughput
		// SLOs per tenant tier; defaults to "unknown" when not supplied
		tenantTier := r.Header.Get("X-Tenant-Tier")
		if tenantTier == "" {
			tenantTier = "unknown"
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", scheme),
			attribute.Int("http.response.status_code", rec.status),
			attribute.String("http.route", route),
			attribute.String("app.tenant.tier", tenantTier),
		}

		outcome := "success"
		if rec.status >= 500 {
			outcome = "error"
			attrs = append(attrs, attribute.String("error.type", strconv.Itoa(rec.status)))
		}

		telemetry.RequestDuration.Record(r.Context(), duration, metric.WithAttributes(attrs...))

		telemetry.RequestOutcome.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("outcome", outcome),
		))

		if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
			if rec.status >= 500 {
				span.SetAttributes(attribute.String("error.type", strconv.Itoa(rec.status)))
			}

			if duration > slowRequestThresholdSeconds {
				span.AddEvent("slow_request", trace.WithAttributes(
					attribute.Float64("http.server.request.duration", duration),
					attribute.String("http.route", route),
				))
			}
		}
	})
}

// statusRecorder wraps http.ResponseWriter to capture the response status code
// for telemetry, while preserving the optional Flusher/Hijacker/ReaderFrom
// interfaces so streaming (SSE), WebSocket upgrades and sendfile keep working.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rec *statusRecorder) WriteHeader(status int) {
	if !rec.wroteHeader {
		rec.status = status
		rec.wroteHeader = true
	}

	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.status = http.StatusOK
		rec.wroteHeader = true
	}

	return rec.ResponseWriter.Write(b)
}

func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rec *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rec.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}

	return h.Hijack()
}

func (rec *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := rec.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}

	return io.Copy(rec.ResponseWriter, src)
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
