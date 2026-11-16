package grpcx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
)

// Defaults for ServerConfig.
const (
	DefaultTimeout         = 10 * time.Second
	DefaultShutdownTimeout = 20 * time.Second
	DefaultMaxConnAge      = 30 * time.Minute
	DefaultMaxConnAgeGrace = 5 * time.Minute
)

// ServerConfig configures NewServer. Zero values take the defaults.
type ServerConfig struct {
	// Env enables reflection when dev or test.
	Env    config.Env
	Logger *slog.Logger // logging.Discard() when nil

	// Verifier authenticates bearer tokens. With none, only public methods
	// can be called.
	Verifier auth.TokenVerifier
	// PublicMethods are full method names ("/skein.v1.IdentityService/SignIn")
	// callable without a token. Health and reflection are always public.
	PublicMethods []string

	// DefaultTimeout is the deadline for unary calls that arrive without
	// one. Negative disables it.
	DefaultTimeout time.Duration
	// RateLimit is called for every call except health checks.
	RateLimit RateLimitFunc

	// ShutdownTimeout bounds the graceful drain in Serve.
	ShutdownTimeout time.Duration
	// MaxConnectionAge and its grace period recycle client connections.
	MaxConnectionAge      time.Duration
	MaxConnectionAgeGrace time.Duration

	// TracerProvider and MeterProvider default to the OpenTelemetry
	// globals that telemetry.Setup installs.
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider

	// Options are appended after the ones NewServer sets.
	Options []grpc.ServerOption
}

// Server is a gRPC server with health checking.
type Server struct {
	*grpc.Server
	// Health is the grpc.health.v1 implementation. Services set a status
	// per service name as their dependencies come and go; "" is the overall
	// status.
	Health *health.Server

	logger          *slog.Logger
	shutdownTimeout time.Duration
}

// NewServer returns a server with the interceptor chain described in the
// package documentation, health registered and serving, and reflection in
// dev. Register service handlers on it, then call Serve.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Logger == nil {
		cfg.Logger = logging.Discard()
	}
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = DefaultTimeout
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	if cfg.MaxConnectionAge == 0 {
		cfg.MaxConnectionAge = DefaultMaxConnAge
	}
	if cfg.MaxConnectionAgeGrace == 0 {
		cfg.MaxConnectionAgeGrace = DefaultMaxConnAgeGrace
	}
	in := &interceptors{
		logger:         cfg.Logger,
		verifier:       cfg.Verifier,
		public:         toSet(cfg.PublicMethods),
		defaultTimeout: cfg.DefaultTimeout,
		rateLimit:      cfg.RateLimit,
	}
	opts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler(otelOptions(cfg.TracerProvider, cfg.MeterProvider)...)),
		grpc.ChainUnaryInterceptor(in.unary),
		grpc.ChainStreamInterceptor(in.stream),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge:      cfg.MaxConnectionAge,
			MaxConnectionAgeGrace: cfg.MaxConnectionAgeGrace,
		}),
	}
	srv := grpc.NewServer(append(opts, cfg.Options...)...)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	if cfg.Env.IsDev() {
		reflection.Register(srv)
	}
	return &Server{Server: srv, Health: hs, logger: cfg.Logger, shutdownTimeout: cfg.ShutdownTimeout}
}

func otelOptions(tp trace.TracerProvider, mp metric.MeterProvider) []otelgrpc.Option {
	var opts []otelgrpc.Option
	if tp != nil {
		opts = append(opts, otelgrpc.WithTracerProvider(tp))
	}
	if mp != nil {
		opts = append(opts, otelgrpc.WithMeterProvider(mp))
	}
	return opts
}

// Serve accepts connections on lis until ctx is cancelled, then drains:
// health turns NOT_SERVING, in-flight calls get ShutdownTimeout to finish,
// and the rest are cut off. It returns nil after a clean shutdown.
func (s *Server) Serve(ctx context.Context, lis net.Listener) error {
	served := make(chan error, 1)
	go func() { served <- s.Server.Serve(lis) }()

	select {
	case err := <-served:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	s.Health.Shutdown()
	stopped := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(stopped)
	}()
	timer := time.NewTimer(s.shutdownTimeout)
	defer timer.Stop()
	select {
	case <-stopped:
	case <-timer.C:
		s.logger.Warn("grpc graceful stop timed out; closing remaining calls",
			slog.Duration("timeout", s.shutdownTimeout))
		// Stop cancels every call's context but does not wait for
		// handlers that ignore cancellation; waiting for them here would
		// make the timeout meaningless. They end with the process.
		s.Stop()
	}
	if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}
