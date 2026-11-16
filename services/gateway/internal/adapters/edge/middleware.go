package edge

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type routeKey struct{}

type routeLabel struct{ route string }

// WithRouteLabel installs the holder LabelRoute fills. The chain calls it
// once, outside the access log.
func WithRouteLabel(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeKey{}, &routeLabel{})))
	})
}

// LabelRoute records the route template of the request in ctx: for the
// access log, as http.route on the server metrics, and on the span.
func LabelRoute(ctx context.Context, method, route string) {
	if l, ok := ctx.Value(routeKey{}).(*routeLabel); ok {
		l.route = route
	}
	if labeler, ok := otelhttp.LabelerFromContext(ctx); ok {
		labeler.Add(attribute.String("http.route", route))
	}
	span := trace.SpanFromContext(ctx)
	span.SetName(method + " " + route)
	span.SetAttributes(attribute.String("http.route", route))
}

// RouteFromContext returns the route recorded by LabelRoute.
func RouteFromContext(ctx context.Context) string {
	if l, ok := ctx.Value(routeKey{}).(*routeLabel); ok {
		return l.route
	}
	return ""
}

// Recover turns a panic into a JSON 500 (google.rpc.Status) and one Error
// line with the stack. http.ErrAbortHandler is re-panicked: it is
// net/http's own way to abort a response.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrote := false
			tracked := httpsnoop.Wrap(w, httpsnoop.Hooks{
				WriteHeader: func(f httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
					return func(code int) { wrote = true; f(code) }
				},
				Write: func(f httpsnoop.WriteFunc) httpsnoop.WriteFunc {
					return func(b []byte) (int, error) { wrote = true; return f(b) }
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
					slog.String("http.method", r.Method), slog.String("http.route", RouteFromContext(r.Context())),
					slog.String("panic", fmt.Sprint(p)), slog.String("stack", string(debug.Stack())))
				if !wrote {
					WriteError(w, r, nil, status.Error(codes.Internal, "panic"))
				}
			}()
			next.ServeHTTP(tracked, r)
		})
	}
}

// AccessLog writes one line per request: method, route template (never
// the raw path or query, which can hold share-link tokens), status, bytes
// and duration_ms. Unrouted requests log a truncated path instead.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
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
			if route := RouteFromContext(r.Context()); route != "" {
				attrs = append(attrs, slog.String("http.route", route))
			} else {
				path := r.URL.Path
				if len(path) > 256 {
					path = path[:256]
				}
				attrs = append(attrs, slog.String("url.path", path))
			}
			log.LogAttrs(r.Context(), level, "http request", attrs...)
		})
	}
}
