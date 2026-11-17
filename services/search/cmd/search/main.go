// Command search runs the search service: SearchService and GraphService
// over gRPC (port 9090), health and readiness over HTTP (port 8080), the
// consumers of documents.events and identity.events, and the follower that
// keeps the in-memory projection in step with the change feed.
//
// Environment (besides the platform's: SKEIN_ENV, DATABASE_URL, REDIS_URL,
// EVENTS_BACKEND and its variables, OTEL_*):
//
//	GRPC_ADDR             gRPC listen address, default :9090
//	ADMIN_ADDR            HTTP health address, default :8080
//	IDENTITY_ADDR         identity gRPC target, default dns:///identity:9090
//	IDENTITY_JWKS_URL     identity's public keys, default http://identity:8080/.well-known/jwks.json
//	ML_ADDR               ml gRPC target; empty disables semantic weights and ml relevance
//	GRPC_CLIENT_INSECURE  dial without TLS, default true (in-cluster without a mesh)
//	GRAPH_INLINE_LIMIT    largest graph whose betweenness and diameter are computed in the request, default 2000
//	FEED_RETENTION        how long change feed entries are kept once a snapshot covers them, default 168h
//	SEARCH_RATE_PER_MIN   per-user in-process limit, the second line behind the gateway, default 600
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/platform/ratelimit"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/platform/shutdown"
	"github.com/genuinebnt/skein-go/platform/telemetry"
	"github.com/genuinebnt/skein-go/services/search/internal/adapters/clients"
	grpcadapter "github.com/genuinebnt/skein-go/services/search/internal/adapters/grpc"
	pgadapter "github.com/genuinebnt/skein-go/services/search/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/search/internal/adapters/pubsub"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/migrations"
)

func main() {
	// The image is distroless (no shell, no curl), so the compose
	// healthcheck runs the binary itself: `search healthcheck` probes
	// /readyz and exits 0 or 1.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(config.FromEnv().String("ADMIN_ADDR", ":8080")))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "search:", err)
		os.Exit(1)
	}
}

type settings struct {
	grpcAddr, adminAddr, identityAddr, jwksURL, mlAddr string
	insecure                                           bool
	inlineLimit, ratePerMinute                         int
	feedRetention                                      time.Duration
}

func run() error {
	l := config.FromEnv()
	svc := config.LoadService(l, "search")
	dbCfg := postgres.LoadConfig(l)
	redisCfg := redisx.LoadConfig(l)
	eventsCfg := events.LoadConfig(l)
	s := settings{
		grpcAddr: l.String("GRPC_ADDR", ":9090"), adminAddr: l.String("ADMIN_ADDR", ":8080"),
		identityAddr: l.String("IDENTITY_ADDR", "dns:///identity:9090"),
		jwksURL:      l.String("IDENTITY_JWKS_URL", "http://identity:8080/.well-known/jwks.json"),
		mlAddr:       l.String("ML_ADDR", ""), insecure: l.Bool("GRPC_CLIENT_INSECURE", true),
		inlineLimit:   l.IntRange("GRAPH_INLINE_LIMIT", 2000, 1, 1<<20),
		ratePerMinute: l.IntRange("SEARCH_RATE_PER_MIN", 600, 1, 1<<20),
		feedRetention: l.Duration("FEED_RETENTION", 7*24*time.Hour),
	}
	if err := l.Err(); err != nil {
		return err
	}

	ctx := context.Background()
	tel, err := telemetry.Setup(ctx, telemetry.Config{Service: svc.Name, Version: svc.Version, Env: string(svc.Env)})
	if err != nil {
		return err
	}
	logger := logging.New(logging.Options{Service: svc.Name, Version: svc.Version, Env: string(svc.Env), Extra: []slog.Handler{tel.LogHandler()}})

	pool, err := postgres.Open(ctx, dbCfg)
	if err != nil {
		return err
	}
	if err := postgres.Migrate(ctx, pool, migrations.FS, ".", logger); err != nil {
		return err
	}
	rdb, err := redisx.Open(ctx, redisCfg)
	if err != nil {
		return err
	}
	cacheStore := redisx.NewRedisStore(rdb, "search")
	invalidator, err := redisx.NewInvalidator(cacheStore, nil)
	if err != nil {
		return err
	}
	eventsCfg.Logger, eventsCfg.TracerProvider = logger, tel.TracerProvider
	bus, err := events.Open(ctx, eventsCfg)
	if err != nil {
		return err
	}

	store := pgadapter.NewStore(pool)
	index := app.NewIndex(store, app.IndexConfig{Logger: logger, OnApplied: func(ctx context.Context, notes []string) {
		tags := make([]string, len(notes))
		for i, n := range notes {
			tags[i] = "note:" + n
		}
		if _, err := invalidator.InvalidateTags(ctx, tags...); err != nil {
			logger.WarnContext(ctx, "cache invalidation failed; entries expire by TTL", "error", err)
		}
	}})
	projector := app.NewProjector(store, index, invalidator, logger)

	identityConn, err := grpcx.NewClient(grpcx.ClientConfig{Target: s.identityAddr, Insecure: s.insecure,
		IdempotentMethods: []string{"skein.v1.IdentityService/ListReadableNotes"}})
	if err != nil {
		return err
	}
	access, err := app.NewAccess(clients.NewIdentity(skeinv1.NewIdentityServiceClient(identityConn)), cacheStore)
	if err != nil {
		return err
	}
	var similarity app.Similarity
	var relevance app.Relevance
	var mlClose func() error
	if s.mlAddr != "" {
		mlConn, err := grpcx.NewClient(grpcx.ClientConfig{Target: s.mlAddr, Insecure: s.insecure, IdempotentMethods: []string{"skein.v1.MLService"}})
		if err != nil {
			return err
		}
		ml := clients.NewML(skeinv1.NewMLServiceClient(mlConn))
		similarity, relevance, mlClose = ml, ml, mlConn.Close
	}
	searcher, err := app.NewSearcher(index, access, cacheStore)
	if err != nil {
		return err
	}
	analyzer := app.NewAnalyzer(app.AnalyzerConfig{InlineLimit: s.inlineLimit, Logger: logger})
	grapher := app.NewGrapher(index, access, analyzer, similarity, relevance, logger)

	keys := auth.NewRemoteKeySet(fetchJWKS(s.jwksURL), auth.RemoteKeySetOptions{})
	srv := grpcx.NewServer(grpcx.ServerConfig{
		Env: svc.Env, Logger: logger, Verifier: auth.NewVerifier(keys, auth.VerifierOptions{}),
		RateLimit: grpcx.RateLimitBySubject(ratelimit.NewTokenBucket(nil), ratelimit.PerMinute(int64(s.ratePerMinute), int64(s.ratePerMinute/6+1))),
	})
	grpcadapter.NewServer(searcher, grapher).Register(srv)

	documents, err := pubsub.NewConsumer(bus, projector.HandleDocuments, pubsub.Config{Subscription: app.DocumentsConsumer, Logger: logger})
	if err != nil {
		return err
	}
	identity, err := pubsub.NewConsumer(bus, projector.HandleIdentity, pubsub.Config{Subscription: app.IdentityConsumer, Logger: logger})
	if err != nil {
		return err
	}

	// The projection is restored before anything is served, so a ready
	// replica never answers from an empty index.
	var ready atomic.Bool
	if err := index.Bootstrap(ctx); err != nil {
		return err
	}
	ready.Store(true)

	grpcLis, err := net.Listen("tcp", s.grpcAddr)
	if err != nil {
		return err
	}
	admin := &http.Server{Addr: s.adminAddr, Handler: adminHandler(&ready, pool), ReadHeaderTimeout: 5 * time.Second}

	m := shutdown.New(shutdown.Options{Logger: logger})
	m.Add("telemetry", nil, tel.Shutdown)
	m.Add("postgres", nil, func(context.Context) error { pool.Close(); return nil })
	m.Add("redis", nil, func(context.Context) error { return rdb.Close() })
	m.Add("events", nil, func(context.Context) error { return bus.Close() })
	m.Add("identity-client", nil, func(context.Context) error { return identityConn.Close() })
	if mlClose != nil {
		m.Add("ml-client", nil, func(context.Context) error { return mlClose() })
	}
	m.Add("feed-follower", index.Run, nil)
	m.Add("graph-analyzer", analyzer.Run, nil)
	m.Add("feed-pruner", func(ctx context.Context) error { return pruneLoop(ctx, store, s.feedRetention, logger) }, nil)
	m.Add("consumer-documents", documents.Run, nil)
	m.Add("consumer-identity", identity.Run, nil)
	m.Add("admin-http", func(context.Context) error {
		if err := admin.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}, admin.Shutdown)
	m.Add("grpc", func(ctx context.Context) error { return srv.Serve(ctx, grpcLis) }, nil)
	logger.Info("search started", "grpc", s.grpcAddr, "admin", s.adminAddr, "ml", s.mlAddr != "")
	return m.Run(ctx)
}

// adminHandler serves liveness (the process runs) and readiness (the
// projection is loaded and Postgres answers).
func adminHandler(ready *atomic.Bool, pool interface{ Ping(context.Context) error }) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if !ready.Load() || pool.Ping(ctx) != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// fetchJWKS reads identity's public keys over HTTP. The contracts have no
// JWKS RPC yet (a platform follow-up), so the URL is configuration.
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
			return nil, fmt.Errorf("jwks: %s", resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
}

func healthcheck(adminAddr string) int {
	_, port, err := net.SplitHostPort(adminAddr)
	if err != nil {
		return 1
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// pruneLoop trims the change feed hourly.
func pruneLoop(ctx context.Context, store *pgadapter.Store, retention time.Duration, logger *slog.Logger) error {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			n, err := store.PruneFeed(ctx, retention)
			if err != nil {
				logger.WarnContext(ctx, "feed prune failed", "error", err)
				continue
			}
			if n > 0 {
				logger.InfoContext(ctx, "feed pruned", "changes", n)
			}
		}
	}
}
