package httpx

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/genuinebnt/skein-go/platform/logging"
)

// RequestIDHeader carries the request ID in both directions.
const RequestIDHeader = "X-Request-ID"

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Config configures Chain.
type Config struct {
	// Operation names the server span ("gateway").
	Operation string
	Logger    *slog.Logger // logging.Discard() when nil
	CORS      CORSConfig
	// HSTS sends Strict-Transport-Security; enable it where TLS is
	// terminated in front of the gateway for every request.
	HSTS bool

	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// Chain returns h wrapped in the standard middleware, in the order in the
// package documentation.
func Chain(cfg Config, h http.Handler) http.Handler {
	if cfg.Logger == nil {
		cfg.Logger = logging.Discard()
	}
	h = CORS(cfg.CORS)(h)
	h = SecurityHeaders(cfg.HSTS)(h)
	h = AccessLog(cfg.Logger)(h)
	h = Recover(cfg.Logger)(h)
	h = RequestID(h)
	var opts []otelhttp.Option
	if cfg.TracerProvider != nil {
		opts = append(opts, otelhttp.WithTracerProvider(cfg.TracerProvider))
	}
	if cfg.MeterProvider != nil {
		opts = append(opts, otelhttp.WithMeterProvider(cfg.MeterProvider))
	}
	return otelhttp.NewHandler(h, cfg.Operation, opts...)
}

// RequestID is step 1.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := logging.RequestIDOrNew(r.Header.Get(RequestIDHeader))
		w.Header().Set(RequestIDHeader, id)
		trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("skein.request_id", id))
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

// Recover is step 2. http.ErrAbortHandler is re-panicked, because it is
// net/http's own signal to abort the response.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wroteHeader := false
			tracked := httpsnoop.Wrap(w, httpsnoop.Hooks{
				WriteHeader: func(f httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
					return func(code int) { wroteHeader = true; f(code) }
				},
				Write: func(f httpsnoop.WriteFunc) httpsnoop.WriteFunc {
					return func(b []byte) (int, error) { wroteHeader = true; return f(b) }
				},
			})
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler {
					panic(p)
				}
				span := trace.SpanFromContext(r.Context())
				span.RecordError(fmt.Errorf("panic: %v", p))
				span.SetStatus(otelcodes.Error, "panic")
				log.ErrorContext(r.Context(), "http handler panicked",
					slog.String("http.method", r.Method),
					slog.String("http.route", r.Pattern),
					slog.String("panic", fmt.Sprint(p)),
					slog.String("stack", string(debug.Stack())))
				if !wroteHeader {
					WriteError(w, r, nil, status.Error(codes.Internal, "panic"))
				}
			}()
			next.ServeHTTP(tracked, r)
		})
	}
}

// AccessLog is step 3.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m := httpsnoop.CaptureMetrics(next, w, r)
			level := slog.LevelInfo
			if m.Code >= 500 {
				level = slog.LevelError
			}
			attrs := []slog.Attr{
				slog.String("http.method", r.Method),
				slog.Int("status", m.Code),
				slog.Int64("bytes", m.Written),
				slog.Float64("duration_ms", math.Round(float64(m.Duration.Microseconds()))/1000),
			}
			// The route pattern is set by ServeMux during routing. Without one
			// (404s) the path is logged instead, truncated; the query string
			// never is.
			if r.Pattern != "" {
				attrs = append(attrs, slog.String("http.route", r.Pattern))
			} else {
				attrs = append(attrs, slog.String("url.path", truncate(r.URL.Path, 256)))
			}
			log.LogAttrs(r.Context(), level, "http request", attrs...)
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SecurityHeaders is step 4.
func SecurityHeaders(hsts bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("Cross-Origin-Resource-Policy", "same-site")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CORSConfig is an allow-list. The zero value allows no cross-origin
// requests, which is right when the frontend is served from the same
// origin as the API.
type CORSConfig struct {
	// AllowedOrigins are exact origins ("https://app.skein.dev"). Wildcards
	// are not supported: with credentials allowed, a wildcard would let any
	// site act as the user.
	AllowedOrigins   []string
	AllowedMethods   []string // default GET, POST, PUT, PATCH, DELETE
	AllowedHeaders   []string // default Authorization, Content-Type, X-Request-ID
	AllowCredentials bool
	MaxAge           time.Duration // preflight cache; default 10 minutes
}

// exposedHeaders are readable by browser code on cross-origin responses.
var exposedHeaders = "X-Request-ID, RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset, Retry-After"

// CORS is step 5.
func CORS(cfg CORSConfig) Middleware {
	methods := cfg.AllowedMethods
	if len(methods) == 0 {
		methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	}
	headers := cfg.AllowedHeaders
	if len(headers) == 0 {
		headers = []string{"Authorization", "Content-Type", RequestIDHeader}
	}
	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = 10 * time.Minute
	}
	allowedMethods := strings.Join(methods, ", ")
	allowedHeaders := strings.Join(headers, ", ")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			// Responses differ by Origin, so caches must key on it even when
			// this request has none.
			w.Header().Add("Vary", "Origin")
			allowed := origin != "" && slices.Contains(cfg.AllowedOrigins, origin)
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			if preflight {
				w.Header().Add("Vary", "Access-Control-Request-Method")
				w.Header().Add("Vary", "Access-Control-Request-Headers")
				if allowed && slices.Contains(methods, r.Header.Get("Access-Control-Request-Method")) {
					setCORSOrigin(w, origin, cfg.AllowCredentials)
					w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
					w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
					w.Header().Set("Access-Control-Max-Age", strconv.Itoa(int(maxAge.Seconds())))
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if allowed {
				setCORSOrigin(w, origin, cfg.AllowCredentials)
				w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func setCORSOrigin(w http.ResponseWriter, origin string, credentials bool) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	if credentials {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
}
