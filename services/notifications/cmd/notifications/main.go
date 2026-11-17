// Command notifications runs the notification service: the
// NotificationService gRPC API on GRPC_ADDR (default :9090), the HTTP admin
// endpoints on HTTP_ADDR (default :8080: /healthz, /readyz), the four event
// consumers, the live fan-out and the email sender. `notifications
// healthcheck` probes /healthz and exits 0 or 1, for the distroless image's
// HEALTHCHECK.
//
// This file is wiring only; see the service README for configuration and
// internal/app for behaviour.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/platform/shutdown"
	"github.com/genuinebnt/skein-go/platform/telemetry"
	grpcadapter "github.com/genuinebnt/skein-go/services/notifications/internal/adapters/grpc"
	"github.com/genuinebnt/skein-go/services/notifications/internal/adapters/identity"
	pgadapter "github.com/genuinebnt/skein-go/services/notifications/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/notifications/internal/adapters/pubsub"
	redisadapter "github.com/genuinebnt/skein-go/services/notifications/internal/adapters/redis"
	"github.com/genuinebnt/skein-go/services/notifications/internal/adapters/smtp"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/email"
	"github.com/genuinebnt/skein-go/services/notifications/migrations"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("HTTP_ADDR")))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "notifications:", err)
		os.Exit(1)
	}
}

// settings is the service's own configuration.
type settings struct {
	grpcAddr, httpAddr  string
	identityTarget      string
	identityJWKSURL     string
	identityToken       config.Secret
	grpcClientInsecure  bool
	appBaseURL          string
	coalesceWindow      time.Duration
	digestDelay         time.Duration
	unreadCacheTTL      time.Duration
	emailMaxAttempts    int
	emailPollInterval   time.Duration
	subscriberBuffer    int
	pubsubMaxDeliveries int
}

func loadSettings(l *config.Loader) settings {
	return settings{
		grpcAddr:            l.String("GRPC_ADDR", ":9090"),
		httpAddr:            l.String("HTTP_ADDR", ":8080"),
		identityTarget:      l.String("IDENTITY_GRPC_TARGET", "dns:///identity:9090"),
		identityJWKSURL:     l.String("IDENTITY_JWKS_URL", "http://identity:8080/.well-known/jwks.json"),
		identityToken:       l.OptionalSecret("IDENTITY_SERVICE_TOKEN"),
		grpcClientInsecure:  l.Bool("GRPC_CLIENT_INSECURE", true),
		appBaseURL:          l.String("APP_BASE_URL", "http://localhost:3000"),
		coalesceWindow:      l.Duration("NOTIFICATIONS_COALESCE_WINDOW", 10*time.Minute),
		digestDelay:         l.Duration("NOTIFICATIONS_DIGEST_DELAY", time.Minute),
		unreadCacheTTL:      l.Duration("NOTIFICATIONS_UNREAD_CACHE_TTL", time.Minute),
		emailMaxAttempts:    l.IntRange("NOTIFICATIONS_EMAIL_MAX_ATTEMPTS", 8, 1, 100),
		emailPollInterval:   l.Duration("NOTIFICATIONS_EMAIL_POLL_INTERVAL", 5*time.Second),
		subscriberBuffer:    l.IntRange("NOTIFICATIONS_SUBSCRIBER_BUFFER", app.DefaultSubscriberBuffer, 1, 10000),
		pubsubMaxDeliveries: l.IntRange("PUBSUB_MAX_DELIVERY_ATTEMPTS", events.DefaultMaxDeliveryAttempts, 1, 100),
	}
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Only fails if the system's random source does.
		panic(fmt.Sprintf("uuid: %v", err))
	}
	return id.String()
}

func run() error {
	l := config.FromEnv()
	svc := config.LoadService(l, "notifications")
	dbCfg := postgres.LoadConfig(l)
	redisCfg := redisx.LoadConfig(l)
	eventsCfg := events.LoadConfig(l)
	smtpCfg := smtp.LoadConfig(l)
	s := loadSettings(l)
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
	redisClient, err := redisx.Open(ctx, redisCfg)
	if err != nil {
		return err
	}
	eventsCfg.Logger, eventsCfg.TracerProvider = logger, tel.TracerProvider
	bus, err := events.Open(ctx, eventsCfg)
	if err != nil {
		return err
	}
	identityConn, err := grpcx.NewClient(grpcx.ClientConfig{Target: s.identityTarget, Insecure: s.grpcClientInsecure,
		IdempotentMethods: []string{"skein.v1.IdentityService/Authorize", "skein.v1.IdentityService/GetUser", "skein.v1.IdentityService/ListMembers"}})
	if err != nil {
		return err
	}

	store := pgadapter.NewStore(pool)
	cacheStore := redisx.NewRedisStore(redisClient, redisadapter.Namespace)
	unread, err := redisadapter.NewUnreadCache(cacheStore, s.unreadCacheTTL, nil)
	if err != nil {
		return err
	}
	fanout := redisadapter.NewFanout(ctx, redisClient, logger)
	hub, err := app.NewHub(fanout, s.subscriberBuffer, logger, nil)
	if err != nil {
		return err
	}
	fanout.SetDeliver(hub.Deliver)
	ident, err := identity.New(skeinv1.NewIdentityServiceClient(identityConn), cacheStore, identity.Options{ServiceToken: s.identityToken})
	if err != nil {
		return err
	}
	renderer, err := email.NewRenderer(s.appBaseURL)
	if err != nil {
		return err
	}
	mailer, err := smtp.New(smtpCfg)
	if err != nil {
		return err
	}
	sender, err := app.NewEmailSender(app.EmailSenderConfig{Store: store, Directory: ident, Mailer: mailer, Renderer: renderer,
		NewID: newID, MaxAttempts: s.emailMaxAttempts, PollInterval: s.emailPollInterval, Logger: logger})
	if err != nil {
		return err
	}
	producer, err := app.NewProducer(app.ProducerConfig{Store: store, Authorizer: ident, Directory: ident, UnreadCache: unread,
		Fanout: fanout, NewID: newID, CoalesceWindow: s.coalesceWindow, DigestDelay: s.digestDelay,
		EmailEnqueued: sender.Notify, Logger: logger})
	if err != nil {
		return err
	}
	consumers, err := pubsub.NewConsumers(producer, pubsub.Options{Invalidator: ident, MaxAttempts: s.pubsubMaxDeliveries, Logger: logger})
	if err != nil {
		return err
	}

	keys := auth.NewRemoteKeySet(fetchJWKS(s.identityJWKSURL), auth.RemoteKeySetOptions{})
	srv := grpcx.NewServer(grpcx.ServerConfig{Env: svc.Env, Logger: logger, Verifier: auth.NewVerifier(keys, auth.VerifierOptions{})})
	grpcadapter.New(app.NewInbox(store, unread, nil, logger), hub, logger).Register(srv)
	grpcLis, err := net.Listen("tcp", s.grpcAddr)
	if err != nil {
		return err
	}
	admin := &http.Server{Addr: s.httpAddr, Handler: adminHandler(pool, redisClient), ReadHeaderTimeout: 5 * time.Second}

	m := shutdown.New(shutdown.Options{Logger: logger})
	m.Add("telemetry", nil, tel.Shutdown)
	m.Add("postgres", nil, func(context.Context) error { pool.Close(); return nil })
	m.Add("redis", nil, func(context.Context) error { return redisClient.Close() })
	m.Add("events", nil, func(context.Context) error { return bus.Close() })
	m.Add("identity-client", nil, func(context.Context) error { return identityConn.Close() })
	m.Add("live-fanout", fanout.Run, nil)
	m.Add("email-sender", sender.Run, nil)
	m.Add("consumers", func(ctx context.Context) error { return consumers.Run(ctx, bus) }, nil)
	m.Add("admin-http", func(ctx context.Context) error { return serveHTTP(ctx, admin) }, nil)
	m.Add("grpc", func(ctx context.Context) error { return srv.Serve(ctx, grpcLis) }, nil)
	logger.Info("notifications starting", slog.String("grpc_addr", s.grpcAddr), slog.String("http_addr", s.httpAddr))
	return m.Run(ctx)
}

// fetchJWKS reads identity's public keys over HTTP for token verification.
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
		return readLimited(resp, 1<<20)
	}
}
