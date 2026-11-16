// Command documents serves DocumentService: notes, live sync, history,
// diff, blame and undo (docs/api/documents.md). It is wiring only: config,
// adapters, servers and shutdown. `documents healthcheck` probes the admin
// port's readiness, for container healthchecks in an image without a shell.
//
// Environment (besides the platform's SKEIN_*, DATABASE_URL, REDIS_URL,
// PUBSUB_* and OTEL_* variables, docs/platform.md):
//
//	DOCUMENTS_GRPC_ADDR        gRPC listener (":9090")
//	DOCUMENTS_ADMIN_ADDR       health HTTP listener (":8080")
//	IDENTITY_GRPC_TARGET       identity's gRPC target ("dns:///identity:9090")
//	IDENTITY_JWKS_URL          identity's public keys ("http://identity:8080/.well-known/jwks.json")
//	DOCUMENTS_AUTH_CACHE_TTL   Authorize decision cache TTL ("10s")
//	DOCUMENTS_NOTE_CACHE_TTL   note metadata cache TTL ("5m")
//	DOCUMENTS_IDLE_TIMEOUT     evict idle note actors after ("5m")
//	DOCUMENTS_DEBOUNCE         BlocksChanged and LinksChanged debounce ("2s")
//	DOCUMENTS_SNAPSHOT_EVERY   ops between snapshots (5000)
//	DOCUMENTS_TRASH_RETENTION  purge trashed notes after ("720h")
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/outbox"
	"github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/platform/shutdown"
	"github.com/genuinebnt/skein-go/platform/telemetry"
	"github.com/genuinebnt/skein-go/services/documents/internal/adapters/admin"
	"github.com/genuinebnt/skein-go/services/documents/internal/adapters/grpcapi"
	"github.com/genuinebnt/skein-go/services/documents/internal/adapters/identity"
	pgstore "github.com/genuinebnt/skein-go/services/documents/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/documents/internal/adapters/pubsub"
	rediscache "github.com/genuinebnt/skein-go/services/documents/internal/adapters/redis"
	"github.com/genuinebnt/skein-go/services/documents/internal/app"
	"github.com/genuinebnt/skein-go/services/documents/migrations"
)

type settings struct {
	service        config.Service
	grpcAddr       string
	adminAddr      string
	identityTarget string
	jwksURL        string
	authTTL        time.Duration
	noteTTL        time.Duration
	app            app.Config
}

func loadSettings(l *config.Loader) settings {
	return settings{
		service:        config.LoadService(l, "documents"),
		grpcAddr:       l.String("DOCUMENTS_GRPC_ADDR", ":9090"),
		adminAddr:      l.String("DOCUMENTS_ADMIN_ADDR", ":8080"),
		identityTarget: l.String("IDENTITY_GRPC_TARGET", "dns:///identity:9090"),
		jwksURL:        l.String("IDENTITY_JWKS_URL", "http://identity:8080/.well-known/jwks.json"),
		authTTL:        l.Duration("DOCUMENTS_AUTH_CACHE_TTL", 10*time.Second),
		noteTTL:        l.Duration("DOCUMENTS_NOTE_CACHE_TTL", 5*time.Minute),
		app: app.Config{
			IdleTimeout:    l.Duration("DOCUMENTS_IDLE_TIMEOUT", 5*time.Minute),
			Debounce:       l.Duration("DOCUMENTS_DEBOUNCE", 2*time.Second),
			SnapshotEvery:  l.IntRange("DOCUMENTS_SNAPSHOT_EVERY", 5000, 100, 1_000_000),
			TrashRetention: l.Duration("DOCUMENTS_TRASH_RETENTION", 30*24*time.Hour),
		},
	}
}

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
		log.Fatal(err) // every bad variable in one message; secrets never printed
	}
	if err := run(context.Background(), s, dbCfg, redisCfg, psCfg); err != nil {
		os.Exit(1)
	}
}

func healthcheck() int {
	addr := config.FromEnv().String("DOCUMENTS_ADMIN_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/readyz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// fetchJWKS reads identity's public keys over HTTP.
func fetchJWKS(url string) auth.FetchFunc {
	client := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("jwks: status %d", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
}

func run(ctx context.Context, s settings, dbCfg postgres.Config, redisCfg redisx.Config, psCfg events.PubSubConfig) error {
	svcInfo := s.service
	tel, err := telemetry.Setup(ctx, telemetry.Config{Service: svcInfo.Name, Version: svcInfo.Version, Env: string(svcInfo.Env)})
	if err != nil {
		log.Printf("telemetry: %v", err)
		return err
	}
	logger := logging.New(logging.Options{Service: svcInfo.Name, Version: svcInfo.Version, Env: string(svcInfo.Env), Extra: []slog.Handler{tel.LogHandler()}})
	fail := func(step string, err error) error {
		logger.Error("startup failed", "step", step, "error", err)
		_ = tel.Shutdown(context.WithoutCancel(ctx))
		return err
	}

	pool, err := postgres.Open(ctx, dbCfg)
	if err != nil {
		return fail("postgres", err)
	}
	if err := postgres.Migrate(ctx, pool, migrations.FS, ".", logger); err != nil {
		return fail("migrate", err)
	}
	rdb, err := redisx.Open(ctx, redisCfg)
	if err != nil {
		return fail("redis", err)
	}
	cacheStore := redisx.NewRedisStore(rdb, "skein")
	bus, err := events.NewPubSub(ctx, psCfg)
	if err != nil {
		return fail("pubsub", err)
	}

	store := pgstore.NewStore(pool)
	relay, err := outbox.NewRelay(outbox.NewPostgresStore(pool, outbox.PostgresStoreOptions{}), bus, outbox.RelayConfig{Logger: logger})
	if err != nil {
		return fail("outbox relay", err)
	}
	store.Notify = relay.Notify

	conn, err := grpcx.NewClient(grpcx.ClientConfig{
		Target:   s.identityTarget,
		Insecure: true,
		IdempotentMethods: []string{
			"skein.v1.IdentityService/Authorize",
			"skein.v1.IdentityService/ListReadableNotes",
			// Registering a replica the caller owns is a no-op, so a retry
			// is safe.
			"skein.v1.IdentityService/RegisterReplica",
		},
	})
	if err != nil {
		return fail("identity client", err)
	}
	identityClient := identity.New(skeinv1.NewIdentityServiceClient(conn))
	authCache, err := rediscache.NewAuthCache(identityClient, cacheStore, s.authTTL)
	if err != nil {
		return fail("authorize cache", err)
	}
	noteCache, err := rediscache.NewNoteCache(cacheStore, s.noteTTL)
	if err != nil {
		return fail("note cache", err)
	}
	documents := app.New(app.Deps{
		Store: store, Authorizer: authCache, AuthCache: authCache, Replicas: identityClient,
		NoteCache: noteCache, Presence: rediscache.NewPresence(rdb, time.Now), Logger: logger,
	}, s.app)

	keys := auth.NewRemoteKeySet(fetchJWKS(s.jwksURL), auth.RemoteKeySetOptions{})
	srv := grpcx.NewServer(grpcx.ServerConfig{Env: svcInfo.Env, Logger: logger, Verifier: auth.NewVerifier(keys, auth.VerifierOptions{})})
	skeinv1.RegisterDocumentServiceServer(srv, grpcapi.New(documents, logger))
	srv.Health.SetServingStatus("skein.v1.DocumentService", healthpb.HealthCheckResponse_SERVING)
	lis, err := net.Listen("tcp", s.grpcAddr)
	if err != nil {
		return fail("listen", err)
	}
	adminServer := &http.Server{
		Addr: s.adminAddr, ReadHeaderTimeout: 5 * time.Second,
		Handler: admin.Handler(
			admin.Check{Name: "postgres", Ready: pool.Ping},
			admin.Check{Name: "redis", Ready: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
		),
	}

	// Components start in dependency order and stop in reverse: the
	// servers drain first, then the note actors flush, then the relay
	// publishes what they wrote, then the clients close.
	m := shutdown.New(shutdown.Options{Logger: logger})
	m.Add("telemetry", nil, tel.Shutdown)
	m.Add("postgres", nil, func(context.Context) error { pool.Close(); return nil })
	m.Add("redis", nil, func(context.Context) error { return rdb.Close() })
	m.Add("pubsub", nil, func(context.Context) error { return bus.Close() })
	m.Add("identity-client", nil, func(context.Context) error { return conn.Close() })
	m.Add("outbox-relay", relay.Run, nil)
	m.Add("note-actors", documents.Run, nil)
	m.Add("identity-events", func(ctx context.Context) error {
		return bus.Subscribe(ctx, pubsub.Subscription, pubsub.Handler(documents, logger))
	}, nil)
	m.Add("admin", func(context.Context) error {
		if err := adminServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}, adminServer.Shutdown)
	m.Add("grpc", func(ctx context.Context) error { return srv.Serve(ctx, lis) }, nil)
	logger.Info("documents starting", "grpc_addr", s.grpcAddr, "admin_addr", s.adminAddr)
	if err := m.Run(ctx); err != nil {
		logger.Error("exit", "error", err)
		return err
	}
	return nil
}
