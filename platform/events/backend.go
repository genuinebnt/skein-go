package events

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/trace"

	"github.com/genuinebnt/skein-go/platform/config"
)

// Backend names, the values of EVENTS_BACKEND.
const (
	BackendPubSub = "pubsub"
	BackendRedis  = "redis"
	BackendMemory = "memory"
)

// Bus is what a service holds: a publisher and subscriber it closes on
// shutdown. PubSub, Redis and Memory implement it.
type Bus interface {
	Publisher
	Subscriber
	Close() error
}

// Provisioner creates topics and subscriptions from a Topology. Memory and
// Redis implement it; Pub/Sub's are created by infrastructure.
type Provisioner interface {
	Provision(ctx context.Context, t Topology) error
}

// Config selects and configures a backend (Open).
type Config struct {
	// Backend is BackendPubSub (the default), BackendRedis or BackendMemory.
	Backend string
	PubSub  PubSubConfig
	Redis   RedisConfig
	// Topology is what Redis and Memory provision; DefaultTopology when
	// empty. Pub/Sub ignores it.
	Topology       Topology
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider
}

// LoadConfig reads EVENTS_BACKEND (pubsub, redis or memory; default
// pubsub) and the selected backend's variables, so a service without
// Google tooling never needs PUBSUB_PROJECT:
//
//	pubsub  PUBSUB_PROJECT (required); PUBSUB_EMULATOR_HOST, read by the client library
//	redis   EVENTS_REDIS_URL, else REDIS_URL (one is required); EVENTS_REDIS_DB (default 0)
//	memory  nothing: in-process only, for tests and a service run alone
func LoadConfig(l *config.Loader) Config {
	cfg := Config{Backend: l.OneOf("EVENTS_BACKEND", BackendPubSub, BackendPubSub, BackendRedis, BackendMemory)}
	switch cfg.Backend {
	case BackendPubSub:
		cfg.PubSub = LoadPubSubConfig(l)
	case BackendRedis:
		cfg.Redis.URL = l.String("EVENTS_REDIS_URL", l.String("REDIS_URL", ""))
		if cfg.Redis.URL == "" {
			l.Fail("EVENTS_REDIS_URL", "EVENTS_BACKEND=redis needs EVENTS_REDIS_URL or REDIS_URL")
		}
		cfg.Redis.Database = l.IntRange("EVENTS_REDIS_DB", 0, 0, 15)
	}
	return cfg
}

// Open connects the selected backend and, for Redis and Memory, provisions
// the topology (creating consumer groups at startup).
func Open(ctx context.Context, cfg Config) (Bus, error) {
	topology := cfg.Topology
	if len(topology.Topics) == 0 {
		topology = DefaultTopology()
	}
	var bus Bus
	switch cfg.Backend {
	case BackendPubSub, "":
		ps := cfg.PubSub
		ps.Logger, ps.TracerProvider = cfg.Logger, cfg.TracerProvider
		return NewPubSub(ctx, ps)
	case BackendRedis:
		rc := cfg.Redis
		rc.Logger, rc.TracerProvider = cfg.Logger, cfg.TracerProvider
		r, err := NewRedis(ctx, rc)
		if err != nil {
			return nil, err
		}
		bus = r
	case BackendMemory:
		bus = NewMemory(MemoryOptions{Logger: cfg.Logger, TracerProvider: cfg.TracerProvider})
	default:
		return nil, fmt.Errorf("events: unknown backend %q", cfg.Backend)
	}
	if err := bus.(Provisioner).Provision(ctx, topology); err != nil {
		_ = bus.Close()
		return nil, err
	}
	return bus, nil
}
