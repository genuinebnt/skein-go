// Command identity runs the identity service: the IdentityService gRPC API,
// the admin HTTP port (JWKS, probes), the outbox relay to identity.events,
// and the documents.events consumer that keeps the note projection current.
// Configuration is environment variables (services/identity/README.md).
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/httpx"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/outbox"
	"github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/platform/ratelimit"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/platform/shutdown"
	"github.com/genuinebnt/skein-go/platform/telemetry"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/services/identity/internal/adapters/admin"
	identityevents "github.com/genuinebnt/skein-go/services/identity/internal/adapters/events"
	identitygrpc "github.com/genuinebnt/skein-go/services/identity/internal/adapters/grpc"
	"github.com/genuinebnt/skein-go/services/identity/internal/adapters/oauth"
	identitypg "github.com/genuinebnt/skein-go/services/identity/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/identity/internal/adapters/pubsub"
	identityredis "github.com/genuinebnt/skein-go/services/identity/internal/adapters/redis"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/keys"
	"github.com/genuinebnt/skein-go/services/identity/migrations"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	l := config.FromEnv()
	s := loadSettings(l)
	dbCfg := postgres.LoadConfig(l)
	redisCfg := redisx.LoadConfig(l)
	psCfg := events.LoadPubSubConfig(l)
	if err := l.Err(); err != nil {
		log.Fatal(err) // every bad variable, in one message; secrets never printed
	}
	if err := run(context.Background(), s, dbCfg, redisCfg, psCfg); err != nil {
		os.Exit(1)
	}
}

// healthcheck probes the admin port's readiness endpoint, for container
// healthchecks in an image that has no shell or curl.
func healthcheck() int {
	addr := config.FromEnv().String("IDENTITY_ADMIN_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/readyz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run(ctx context.Context, s settings, dbCfg postgres.Config, redisCfg redisx.Config, psCfg events.PubSubConfig) error {
	svc := s.Service
	tel, err := telemetry.Setup(ctx, telemetry.Config{Service: svc.Name, Version: svc.Version, Env: string(svc.Env)})
	if err != nil {
		log.Printf("telemetry: %v", err)
		return err
	}
	logger := logging.New(logging.Options{Service: svc.Name, Version: svc.Version, Env: string(svc.Env), Extra: []slog.Handler{tel.LogHandler()}})
	fail := func(what string, err error) error {
		logger.Error("startup failed", "step", what, "error", err)
		return err
	}
	now := s.clock()

	dbCfg.TracerProvider, dbCfg.MeterProvider = tel.TracerProvider, tel.MeterProvider
	pool, err := postgres.Open(ctx, dbCfg)
	if err != nil {
		return fail("postgres", err)
	}
	if err := postgres.Migrate(ctx, pool, migrations.FS, ".", logger); err != nil {
		return fail("migrate", err)
	}
	redisCfg.TracerProvider, redisCfg.MeterProvider = tel.TracerProvider, tel.MeterProvider
	rdb, err := redisx.Open(ctx, redisCfg)
	if err != nil {
		return fail("redis", err)
	}
	psCfg.Logger, psCfg.TracerProvider = logger, tel.TracerProvider
	bus, err := events.NewPubSub(ctx, psCfg)
	if err != nil {
		return fail("pubsub", err)
	}

	ring, err := s.keyring(now)
	if err != nil {
		return fail("keys", err)
	}
	hasher, err := credential.NewHasher(credential.HasherOptions{Params: s.Argon2, MaxConcurrent: s.Argon2Concurrency})
	if err != nil {
		return fail("argon2", err)
	}
	cache, err := identityredis.NewDecisionCache(redisx.NewRedisStore(rdb, "identity"), s.App.DecisionTTL, tel.MeterProvider)
	if err != nil {
		return fail("cache", err)
	}
	store := identitypg.NewStore(pool)
	relay, err := outbox.NewRelay(outbox.NewPostgresStore(pool, outbox.PostgresStoreOptions{}), bus,
		outbox.RelayConfig{Logger: logger, MeterProvider: tel.MeterProvider})
	if err != nil {
		return fail("relay", err)
	}
	store.OnEnqueue = relay.Notify

	providers := map[app.OAuthProviderKind]app.OAuthProvider{}
	if s.Google.ID != "" {
		providers[app.OAuthGoogle] = oauth.Google{Client: oauth.Client{ID: s.Google.ID, Secret: s.Google.Secret, Endpoints: oauth.GoogleEndpoints}}
	}
	if s.GitHub.ID != "" {
		providers[app.OAuthGitHub] = oauth.GitHub{Client: oauth.Client{ID: s.GitHub.ID, Secret: s.GitHub.Secret, Endpoints: oauth.GitHubEndpoints}}
	}
	service, err := app.New(s.App, app.Deps{
		Store: store, Hasher: hasher, Issuer: ring, Cache: cache,
		Limiter: ratelimit.NewRedisGCRA(rdb, "identity:ratelimit"), Providers: providers,
		Clock: now, Logger: logger, MeterProvider: tel.MeterProvider,
	})
	if err != nil {
		return fail("app", err)
	}
	consumer, err := pubsub.NewConsumer(service, identityevents.DocumentsSubscription, logger, tel.MeterProvider)
	if err != nil {
		return fail("consumer", err)
	}

	grpcServer := grpcx.NewServer(grpcx.ServerConfig{
		Env: svc.Env, Logger: logger,
		Verifier:       auth.NewVerifier(ring, auth.VerifierOptions{Now: now}),
		PublicMethods:  identitygrpc.PublicMethods(s.App.AllowUnauthenticatedInternalCalls),
		TracerProvider: tel.TracerProvider, MeterProvider: tel.MeterProvider,
	})
	skeinv1.RegisterIdentityServiceServer(grpcServer, identitygrpc.NewServer(service, logger))
	grpcLis, err := net.Listen("tcp", s.GRPCAddr)
	if err != nil {
		return fail("grpc listen", err)
	}
	var devSessions admin.DevSessions
	if s.App.DevSessions {
		logger.Warn("IDENTITY_DEV_SESSIONS is on: POST " + admin.DevSessionPath + " on the admin port signs in any user without a credential")
		devSessions = service
	}
	adminServer := &http.Server{
		Addr:              s.AdminAddr,
		Handler:           httpx.Chain(httpx.Config{Operation: "identity-admin", Logger: logger, TracerProvider: tel.TracerProvider, MeterProvider: tel.MeterProvider}, admin.Handler(ring, pool.Ping, logger, devSessions)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Added in dependency order; stopped in reverse: servers drain first,
	// then the consumer and relay, then clients, and telemetry flushes last.
	m := shutdown.New(shutdown.Options{Logger: logger})
	m.Add("telemetry", nil, tel.Shutdown)
	m.Add("postgres", nil, func(context.Context) error { pool.Close(); return nil })
	m.Add("redis", nil, func(context.Context) error { return rdb.Close() })
	m.Add("pubsub", nil, func(context.Context) error { return bus.Close() })
	m.Add("outbox-relay", relay.Run, nil)
	m.Add("documents-events", func(ctx context.Context) error {
		return bus.Subscribe(ctx, identityevents.DocumentsSubscription, consumer.Handle)
	}, nil)
	if s.KeyRotation > 0 {
		logger.Warn("in-process key rotation enabled; correct for a single replica only", "interval", s.KeyRotation.String())
		m.Add("key-rotation", (&keys.Rotator{Ring: ring, Interval: s.KeyRotation}).Run, nil)
	}
	m.Add("admin-http", func(ctx context.Context) error {
		err := adminServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}, adminServer.Shutdown)
	m.Add("grpc", func(ctx context.Context) error { return grpcServer.Serve(ctx, grpcLis) }, nil)
	logger.Info("identity starting", "grpc_addr", s.GRPCAddr, "admin_addr", s.AdminAddr,
		"oauth_providers", len(providers), "fixed_clock", !s.FixedNow.IsZero())
	if err := m.Run(ctx); err != nil {
		logger.Error("exit", "error", err)
		return err
	}
	return nil
}
