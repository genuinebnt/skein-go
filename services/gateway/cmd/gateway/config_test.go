package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

func TestConfigOverridesOneLimitAndKeepsTheRest(t *testing.T) {
	l := config.FromMap(map[string]string{
		"RATE_LIMIT_SEARCH_USER": "30/1m:5",
		"TRUSTED_PROXIES":        "10.0.0.0/8, 192.168.1.1",
		"CORS_ALLOWED_ORIGINS":   "https://app.skein.dev,http://localhost:3000",
	})
	cfg := loadConfig(l, config.EnvProd)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Limits[domain.BudgetKey{Budget: domain.BudgetSearch, Subject: domain.SubjectUser}]; got != (ratelimit.Limit{Rate: 30, Period: time.Minute, Burst: 5}) {
		t.Errorf("search per user = %+v", got)
	}
	if got := cfg.Limits[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectUser}]; got != domain.DefaultLimits()[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectUser}] {
		t.Errorf("api per user changed: %+v", got)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.HTTPAddr != ":8080" || cfg.Targets.Identity != "dns:///identity:9090" {
		t.Errorf("config %+v", cfg)
	}
	if hosts := originHosts(cfg.CORSOrigins); len(hosts) != 2 || hosts[0] != "app.skein.dev" || hosts[1] != "localhost:3000" {
		t.Errorf("origin hosts %v", hosts)
	}
}

func TestConfigReportsEveryBadValue(t *testing.T) {
	l := config.FromMap(map[string]string{"RATE_LIMIT_AGENT_IP": "lots", "TRUSTED_PROXIES": "not-a-cidr/99"})
	loadConfig(l, config.EnvProd)
	err := l.Err()
	if !errors.Is(err, config.ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	for _, key := range []string{"RATE_LIMIT_AGENT_IP", "TRUSTED_PROXIES"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}

func TestConfigAllowsTheViteDevServerOnlyInDevWhenOriginsAreUnset(t *testing.T) {
	cases := []struct {
		env  config.Env
		vars map[string]string
		want []string
	}{
		{config.EnvDev, map[string]string{}, []string{"http://localhost:5173"}},
		{config.EnvTest, map[string]string{}, []string{"http://localhost:5173"}},
		{config.EnvProd, map[string]string{}, nil},
		{config.EnvStaging, map[string]string{}, nil},
		{config.EnvDev, map[string]string{"CORS_ALLOWED_ORIGINS": "https://app.skein.dev"}, []string{"https://app.skein.dev"}},
	}
	for _, c := range cases {
		l := config.FromMap(c.vars)
		cfg := loadConfig(l, c.env)
		if err := l.Err(); err != nil {
			t.Fatal(err)
		}
		if strings.Join(cfg.CORSOrigins, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s %v: origins %v, want %v", c.env, c.vars, cfg.CORSOrigins, c.want)
		}
	}
}
