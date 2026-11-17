package main

import (
	"net/netip"
	"strings"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/backends"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// gatewayConfig is everything the gateway reads from the environment
// besides the platform's own variables (SKEIN_ENV, REDIS_URL,
// PUBSUB_PROJECT, OTEL_*). services/gateway/README.md lists them.
type gatewayConfig struct {
	HTTPAddr       string
	Targets        backends.Targets
	DocumentsSync  string
	GRPCInsecure   bool
	JWKSURL        string
	CORSOrigins    []string
	TrustedProxies []netip.Prefix
	HSTS           bool
	DrainDelay     time.Duration
	RequestTimeout time.Duration
	Limits         domain.Limits
	RedisNamespace string
}

// devOrigins are allowed when CORS_ALLOWED_ORIGINS is unset in dev and test:
// the web frontend's Vite dev server (web/), so `npm run dev` works against
// a local gateway without configuration. Never applied in staging or prod.
var devOrigins = []string{"http://localhost:5173"}

func loadConfig(l *config.Loader, env config.Env) gatewayConfig {
	cfg := gatewayConfig{
		HTTPAddr: l.String("GATEWAY_HTTP_ADDR", ":8080"),
		Targets: backends.Targets{
			Identity:      l.String("IDENTITY_GRPC_TARGET", "dns:///identity:9090"),
			Documents:     l.String("DOCUMENTS_GRPC_TARGET", "dns:///documents:9090"),
			Search:        l.String("SEARCH_GRPC_TARGET", "dns:///search:9090"),
			ML:            l.String("ML_GRPC_TARGET", "dns:///ml:9090"),
			Agent:         l.String("AGENT_GRPC_TARGET", "dns:///agent:9090"),
			Notifications: l.String("NOTIFICATIONS_GRPC_TARGET", "dns:///notifications:9090"),
		},
		DocumentsSync:  l.String("DOCUMENTS_SYNC_ADDR", "documents:9090"),
		GRPCInsecure:   l.Bool("GRPC_INSECURE", true),
		JWKSURL:        l.String("IDENTITY_JWKS_URL", "http://identity:8080/.well-known/jwks.json"),
		CORSOrigins:    l.List("CORS_ALLOWED_ORIGINS", nil),
		HSTS:           l.Bool("GATEWAY_HSTS", false),
		DrainDelay:     l.Duration("GATEWAY_DRAIN_DELAY", 5*time.Second),
		RequestTimeout: l.Duration("GATEWAY_REQUEST_TIMEOUT", 30*time.Second),
		RedisNamespace: l.String("GATEWAY_REDIS_NAMESPACE", "gw"),
		Limits:         domain.DefaultLimits(),
	}
	if cfg.CORSOrigins == nil && env.IsDev() {
		cfg.CORSOrigins = devOrigins
	}
	proxies, err := domain.ParsePrefixes(l.List("TRUSTED_PROXIES", nil))
	if err != nil {
		l.Fail("TRUSTED_PROXIES", err.Error())
	}
	cfg.TrustedProxies = proxies
	// RATE_LIMIT_<BUDGET>_<USER|IP>=rate/period:burst overrides one limit,
	// for example RATE_LIMIT_SEARCH_USER=120/1m:30.
	for _, budget := range domain.Budgets {
		for _, subject := range []domain.SubjectKind{domain.SubjectUser, domain.SubjectIP} {
			key := "RATE_LIMIT_" + strings.ToUpper(string(budget)) + "_" + strings.ToUpper(string(subject))
			value := l.String(key, "")
			if value == "" {
				continue
			}
			limit, err := domain.ParseLimit(value)
			if err != nil {
				l.Fail(key, err.Error())
				continue
			}
			cfg.Limits[domain.BudgetKey{Budget: budget, Subject: subject}] = limit
		}
	}
	return cfg
}

// originHosts turns allowed origins ("https://app.skein.dev") into the
// host patterns coder/websocket checks Origin against.
func originHosts(origins []string) []string {
	hosts := make([]string, 0, len(origins))
	for _, o := range origins {
		if _, host, ok := strings.Cut(o, "://"); ok {
			hosts = append(hosts, host)
		} else {
			hosts = append(hosts, o)
		}
	}
	return hosts
}
