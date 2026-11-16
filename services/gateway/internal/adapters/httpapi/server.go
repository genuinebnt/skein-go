package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/genuinebnt/skein-go/platform/httpx"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/edge"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/wsapi"
	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
)

// Check is one readiness diagnostic.
type Check func(ctx context.Context) error

// ServerConfig configures NewServer.
type ServerConfig struct {
	REST      http.Handler
	WebSocket *wsapi.Handlers
	Registry  *app.Registry
	CORS      httpx.CORSConfig
	HSTS      bool
	Logger    *slog.Logger
	// Checks are reported by /readyz but do not make it fail: Redis and
	// the key set both fail open or degrade gracefully, and a readiness
	// probe that failed with them would take every replica out at once.
	Checks map[string]Check
	// DrainDelay is how long readiness reports 503 before the listener
	// closes; zero in tests, a few seconds in Kubernetes.
	DrainDelay time.Duration

	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// Server is the gateway's HTTP server.
type Server struct {
	cfg      ServerConfig
	http     *http.Server
	draining atomic.Bool
}

// NewServer assembles the handler chain (see the package documentation).
func NewServer(cfg ServerConfig) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: cfg}
	router := http.NewServeMux()
	router.HandleFunc("GET /healthz", s.healthz)
	router.HandleFunc("GET /readyz", s.readyz)
	router.HandleFunc("GET /openapi.json", serveOpenAPI)
	router.HandleFunc("GET /docs", serveDocs)
	cfg.WebSocket.Register(router)
	router.Handle("/", cfg.REST)

	cors := cfg.CORS
	if len(cors.AllowedHeaders) == 0 {
		cors.AllowedHeaders = []string{"Authorization", "Content-Type", httpx.RequestIDHeader,
			headerIdempotencyKey, "traceparent", "tracestate"}
	}
	var h http.Handler = router
	h = exposeReplayHeader(h)
	h = httpx.CORS(cors)(h)
	h = httpx.SecurityHeaders(cfg.HSTS)(h)
	h = edge.AccessLog(cfg.Logger)(h)
	h = edge.Recover(cfg.Logger)(h)
	h = edge.WithRouteLabel(h)
	h = httpx.RequestID(h)
	var opts []otelhttp.Option
	if cfg.TracerProvider != nil {
		opts = append(opts, otelhttp.WithTracerProvider(cfg.TracerProvider))
	}
	if cfg.MeterProvider != nil {
		opts = append(opts, otelhttp.WithMeterProvider(cfg.MeterProvider))
	}
	h = otelhttp.NewHandler(h, "gateway", opts...)
	s.http = &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
	}
	return s
}

// Handler returns the full handler chain, for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// exposeReplayHeader lets browser code read Idempotent-Replayed, which
// httpx.CORS's fixed exposed list lacks (a platform follow-up).
func exposeReplayHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exposed := w.Header().Get("Access-Control-Expose-Headers"); exposed != "" {
			w.Header().Set("Access-Control-Expose-Headers", exposed+", "+headerReplayed)
		}
		next.ServeHTTP(w, r)
	})
}

// Serve serves lis until Drain stops it (it then returns nil) or ctx ends
// (it then closes every connection at once). Register Drain as the
// shutdown stop function so the drain happens first.
func (s *Server) Serve(ctx context.Context, lis net.Listener) error {
	errs := make(chan error, 1)
	go func() { errs <- s.http.Serve(lis) }()
	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		_ = s.http.Close()
		<-errs
		return nil
	}
}

// Drain stops the server gracefully within ctx (see the package
// documentation).
func (s *Server) Drain(ctx context.Context) error {
	s.draining.Store(true)
	s.cfg.Logger.InfoContext(ctx, "draining", slog.Int("websockets", s.cfg.Registry.Len()))
	select {
	case <-time.After(s.cfg.DrainDelay):
	case <-ctx.Done():
	}
	shutdown := make(chan error, 1)
	go func() { shutdown <- s.http.Shutdown(ctx) }()
	socketsErr := s.cfg.Registry.Drain(ctx)
	runsErr := s.cfg.WebSocket.StopRuns(ctx)
	return errors.Join(<-shutdown, socketsErr, runsErr)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	edge.LabelRoute(r.Context(), r.Method, "/healthz")
	writeHealth(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	edge.LabelRoute(r.Context(), r.Method, "/readyz")
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	names := make([]string, 0, len(s.cfg.Checks))
	for name := range s.cfg.Checks {
		names = append(names, name)
	}
	sort.Strings(names)
	checks := map[string]string{}
	for _, name := range names {
		if err := s.cfg.Checks[name](ctx); err != nil {
			checks[name] = "degraded: " + firstLine(err.Error())
		} else {
			checks[name] = "ok"
		}
	}
	if s.draining.Load() {
		writeHealth(w, http.StatusServiceUnavailable, map[string]any{"status": "draining", "checks": checks})
		return
	}
	writeHealth(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func writeHealth(w http.ResponseWriter, code int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
