package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"
)

func TestLoadConfigAsksOnlyForTheSelectedBackendsVariables(t *testing.T) {
	for name, tc := range map[string]struct {
		env     map[string]string
		backend string
		wantErr string
	}{
		"default is pubsub and needs a project": {env: map[string]string{}, wantErr: "PUBSUB_PROJECT"},
		"pubsub with a project":                 {env: map[string]string{"PUBSUB_PROJECT": "p"}, backend: BackendPubSub},
		"redis needs no Google variable":        {env: map[string]string{"EVENTS_BACKEND": "redis", "REDIS_URL": "redis://h:1/3"}, backend: BackendRedis},
		"redis without a url":                   {env: map[string]string{"EVENTS_BACKEND": "redis"}, wantErr: "EVENTS_REDIS_URL"},
		"memory needs nothing":                  {env: map[string]string{"EVENTS_BACKEND": "memory"}, backend: BackendMemory},
		"unknown backend":                       {env: map[string]string{"EVENTS_BACKEND": "kafka", "PUBSUB_PROJECT": "p"}, wantErr: "EVENTS_BACKEND"},
	} {
		l := config.FromMap(tc.env)
		cfg := LoadConfig(l)
		err := l.Err()
		switch {
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: error %v, want one naming %s", name, err, tc.wantErr)
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.wantErr == "" && cfg.Backend != tc.backend:
			t.Errorf("%s: backend %q, want %q", name, cfg.Backend, tc.backend)
		}
	}
}

func TestLoadConfigPrefersEventsRedisURLAndIgnoresTheCacheDatabase(t *testing.T) {
	cfg := LoadConfig(config.FromMap(map[string]string{"EVENTS_BACKEND": "redis", "REDIS_URL": "redis://cache:6379/2"}))
	if cfg.Redis.URL != "redis://cache:6379/2" || cfg.Redis.Database != 0 {
		t.Errorf("got %+v, want REDIS_URL with database 0", cfg.Redis)
	}
	cfg = LoadConfig(config.FromMap(map[string]string{"EVENTS_BACKEND": "redis", "REDIS_URL": "redis://cache/2", "EVENTS_REDIS_URL": "redis://bus", "EVENTS_REDIS_DB": "4"}))
	if cfg.Redis.URL != "redis://bus" || cfg.Redis.Database != 4 {
		t.Errorf("got %+v, want EVENTS_REDIS_URL with database 4", cfg.Redis)
	}
}

func TestOpenMemoryProvisionsTheDefaultTopology(t *testing.T) {
	bus, err := Open(context.Background(), Config{Backend: BackendMemory})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if err := bus.Publish(context.Background(), "documents.events", envelope(t, "n1", "x")); err != nil {
		t.Fatal(err)
	}
	m := bus.(*Memory)
	for _, sub := range []string{"search.documents-events", "ml.documents-events", "notifications.documents-events", "identity.documents-events"} {
		if m.Pending(sub) != 1 {
			t.Errorf("%s has %d pending, want 1", sub, m.Pending(sub))
		}
	}
}

func TestOpenRedisProvisionsConsumerGroupsAtStartup(t *testing.T) {
	mr, _ := newMiniredis(t)
	bus, err := Open(context.Background(), Config{Backend: BackendRedis, Redis: RedisConfig{URL: "redis://" + mr.Addr() + "/5", PollInterval: 20 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	// Database 5 in the URL is ignored: events are in database 0.
	mr.Select(0)
	if !mr.Exists(RedisStreamKey("identity.events", 0)) {
		t.Fatal("identity.events partition 0 was not created in database 0")
	}
}
