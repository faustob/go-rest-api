// ----------------------------------------------------------------------------
// HTTP middleware that records the standard http.server.request.duration
// histogram plus request-outcome/tenant and auth-outcome counters, for the
// go-rest-api chi router.
// ----------------------------------------------------------------------------

package telemetry

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// slowRequestBudget is the P99 latency budget; requests exceeding it get a
// "slow.request" span event recorded for triage.
const slowRequestBudget = 750 * time.Millisecond

// statusRecorder wraps http.ResponseWriter to capture the status code while
// forwarding the optional Flusher/Hijacker/ReaderFrom interfaces with their
// exact signatures, so streaming, SSE and websocket upgrades keep working.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// HTTPMetricsMiddleware records the http.server.request.duration histogram
// (OTel semantic convention, in seconds) plus a request outcome/tenant
// counter for every request, and wraps the request in a server span so
// downstream calls nest under it. Mount with router.Use(...) so it observes
// every route uniformly.
func HTTPMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}

		ctx, span := Tracer.Start(req.Context(), "HTTP "+req.Method)
		req = req.WithContext(ctx)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, req)

		duration := time.Since(start)

		// RoutePattern is only populated by chi AFTER routing has happened,
		// so it must be read here, never before next.ServeHTTP.
		route := ""
		if rctx := chi.RouteContext(req.Context()); rctx != nil {
			route = rctx.RoutePattern()
		}

		outcome := "success"
		errType := ""
		if rec.status >= 500 {
			outcome = "failure"
			errType = "server_error_" + strconv.Itoa(rec.status)
		} else if rec.status >= 400 {
			outcome = "failure"
			errType = "client_error_" + strconv.Itoa(rec.status)
		}

		tenant := req.Header.Get("X-Tenant-ID")
		if tenant == "" {
			tenant = req.Header.Get("X-API-Key")
		}
		if tenant == "" {
			tenant = "unknown"
		}

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(req.Method),
			semconv.URLScheme(scheme),
			semconv.HTTPResponseStatusCode(rec.status),
		}
		if route != "" {
			attrs = append(attrs, semconv.HTTPRoute(route))
		}
		if errType != "" {
			attrs = append(attrs, semconv.ErrorTypeKey.String(errType))
		}

		HTTPRequestDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))

		counterAttrs := append(append([]attribute.KeyValue{}, attrs...),
			attribute.String("outcome", outcome),
			attribute.String("tenant.id", tenant),
		)
		HTTPRequestCounter.Add(ctx, 1, metric.WithAttributes(counterAttrs...))

		span.SetAttributes(attrs...)
		if rec.status >= 500 {
			span.SetStatus(codes.Error, errType)
		}
		if duration > slowRequestBudget {
			span.AddEvent("slow.request", trace.WithAttributes(
				attribute.Int64("duration_ms", duration.Milliseconds()),
				attribute.String("http.route", route),
			))
		}
		span.End()
	})
}

// AuthOutcomeMiddleware records every authentication/authorization decision
// as an auth.attempts counter, tagged with the outcome and a low-cardinality
// denial reason (the response status). It MUST be mounted BEFORE the JWT
// validator middleware in the protected router group, so it also observes
// the validator's own rejections (which would otherwise short-circuit the
// chain before any outcome-observing middleware placed after it).
func AuthOutcomeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, req)

		outcome := "allowed"
		reason := "ok"
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			outcome = "denied"
			reason = strconv.Itoa(rec.status)
		}

		AuthAttemptsCounter.Add(req.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("reason", reason),
		))
	})
}
