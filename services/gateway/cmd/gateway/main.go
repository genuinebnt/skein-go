// Command gateway is Skein's only public entry point: REST/JSON through
// grpc-gateway and the three WebSocket streams, in front of the gRPC
// services (ADR-0002). This file is wiring only; see services/gateway's
// README and the internal packages' documentation.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/httpx"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/ratelimit"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/platform/shutdown"
	"github.com/genuinebnt/skein-go/platform/telemetry"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/backends"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/edge"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/httpapi"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/identityevents"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/jwks"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/redisstore"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/wsapi"
	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(1)
	}
}

func run() error {
	l := config.FromEnv()
	svc := config.LoadService(l, "gateway")
	cfg := loadConfig(l, svc.Env)
	redisCfg := redisx.LoadConfig(l)
	eventsCfg := events.LoadConfig(l)
	if err := l.Err(); err != nil {
		return err
	}
	if err := cfg.Limits.Validate(); err != nil {
		return err
	}

	ctx := context.Background()
	tel, err := telemetry.Setup(ctx, telemetry.Config{Service: svc.Name, Version: svc.Version, Env: string(svc.Env)})
	if err != nil {
		return err
	}
	logger := logging.New(logging.Options{Service: svc.Name, Version: svc.Version, Env: string(svc.Env),
		Extra: []slog.Handler{tel.LogHandler()}})

	redisCfg.TracerProvider, redisCfg.MeterProvider = tel.TracerProvider, tel.MeterProvider
	rdb, err := redisx.Open(ctx, redisCfg)
	if err != nil {
		return err
	}
	eventsCfg.Logger, eventsCfg.TracerProvider = logger, tel.TracerProvider
	bus, err := events.Open(ctx, eventsCfg)
	if err != nil {
		return err
	}

	keys := auth.NewRemoteKeySet(jwks.Fetch(cfg.JWKSURL, nil), auth.RemoteKeySetOptions{})
	verifier := auth.NewVerifier(keys, auth.VerifierOptions{})
	throttler, err := app.NewThrottler(ratelimit.NewRedisGCRA(rdb, cfg.RedisNamespace+":rl"), cfg.Limits, logger, tel.MeterProvider)
	if err != nil {
		return err
	}
	idempotency, err := app.NewIdempotency(redisstore.NewIdempotencyStore(rdb, cfg.RedisNamespace),
		app.IdempotencyOptions{Logger: logger, Meter: tel.MeterProvider, PendingTTL: cfg.RequestTimeout + time.Minute})
	if err != nil {
		return err
	}
	registry := app.NewRegistry()
	revocations := app.NewRevocations(redisstore.NewRevocationBus(rdb, cfg.RedisNamespace, logger), registry, logger)

	dialOpts := backends.DialOptions{Insecure: cfg.GRPCInsecure, TracerProvider: tel.TracerProvider, MeterProvider: tel.MeterProvider}
	conns, open, err := backends.Dial(cfg.Targets, dialOpts)
	if err != nil {
		return err
	}
	clients := backends.NewClients(conns)
	syncRouter, err := backends.NewSyncRouter(backends.SyncRouterOptions{
		HostPort: cfg.DocumentsSync,
		Dial: func(addr string) (*grpc.ClientConn, error) {
			return grpcx.NewClient(grpcx.ClientConfig{Target: "passthrough:///" + addr, Insecure: cfg.GRPCInsecure,
				TracerProvider: tel.TracerProvider, MeterProvider: tel.MeterProvider})
		},
		OnChange: func(owner func(string) (string, bool)) {
			if n := registry.Reroute(owner); n > 0 {
				logger.Info("closed sync sockets whose note moved", slog.Int("closed", n))
			}
		},
		Logger: logger,
	})
	if err != nil {
		return err
	}
	if err := syncRouter.Refresh(ctx); err != nil {
		logger.Warn("documents instances not resolved yet; sync connects once they are", slog.String("error", err.Error()))
	}

	routes, err := httpapi.LoadRoutes()
	if err != nil {
		return err
	}
	clientIP := edge.ClientIPBehind(cfg.TrustedProxies)
	rest, err := httpapi.NewREST(ctx, httpapi.RESTConfig{
		Clients: clients, Routes: routes, Verifier: verifier, Throttler: throttler, Idempotency: idempotency,
		ClientIP: clientIP, Logger: logger, RequestTimeout: cfg.RequestTimeout,
	})
	if err != nil {
		return err
	}
	ws, err := wsapi.New(wsapi.Config{
		Verifier: verifier, Throttler: throttler, ClientIP: clientIP, Registry: registry,
		Sync: syncRouter, NoteAccess: backends.NoteAccess{Identity: clients.Identity},
		Agent: clients.Agent, Notifications: clients.Notifications,
		OriginPatterns: originHosts(cfg.CORSOrigins), Logger: logger, Meter: tel.MeterProvider,
	})
	if err != nil {
		return err
	}
	server := httpapi.NewServer(httpapi.ServerConfig{
		REST: rest, WebSocket: ws, Registry: registry, HSTS: cfg.HSTS, Logger: logger, DrainDelay: cfg.DrainDelay,
		CORS: httpx.CORSConfig{AllowedOrigins: cfg.CORSOrigins},
		Checks: map[string]httpapi.Check{
			"redis": func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		},
		TracerProvider: tel.TracerProvider, MeterProvider: tel.MeterProvider,
	})
	lis, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	logger.Info("gateway listening", slog.String("addr", lis.Addr().String()), slog.Int("routes", len(routes.Routes())))

	// Components stop in reverse: the HTTP server drains first, then the
	// event consumers, then the clients, and telemetry flushes last.
	m := shutdown.New(shutdown.Options{Logger: logger})
	m.Add("telemetry", nil, tel.Shutdown)
	m.Add("redis", nil, func(context.Context) error { return rdb.Close() })
	m.Add("events", nil, func(context.Context) error { return bus.Close() })
	m.Add("backends", nil, func(context.Context) error {
		for _, c := range open {
			_ = c.Close()
		}
		return nil
	})
	m.Add("sync-router", syncRouter.Run, nil)
	m.Add("revocation-broadcasts", keepRunning(logger, "revocation-broadcasts", revocations.Listen), nil)
	m.Add("identity-events", keepRunning(logger, "identity-events", func(ctx context.Context) error {
		return bus.Subscribe(ctx, identityevents.Subscription, identityevents.Handler(revocations, logger))
	}), nil)
	m.Add("http", func(ctx context.Context) error { return server.Serve(ctx, lis) }, server.Drain)
	return m.Run(ctx)
}

// keepRunning restarts a consumer that returns early (Redis or Pub/Sub
// unreachable) with capped exponential backoff, instead of letting the
// shutdown manager stop the gateway: losing revocation pushes for a while
// is a degradation (documents still re-checks), not an outage.
func keepRunning(log *slog.Logger, name string, run func(ctx context.Context) error) shutdown.RunFunc {
	return func(ctx context.Context) error {
		backoff := time.Second
		for {
			start := time.Now()
			err := run(ctx)
			if ctx.Err() != nil {
				return nil
			}
			if time.Since(start) > time.Minute {
				backoff = time.Second
			}
			msg := "<nil>"
			if err != nil {
				msg = err.Error()
			}
			log.Warn("consumer stopped; restarting", slog.String("component", name), slog.String("error", msg), slog.Duration("backoff", backoff))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(2*backoff, 30*time.Second)
		}
	}
}
