package redisx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/genuinebnt/skein-go/platform/config"
)

// Config configures Open.
type Config struct {
	URL config.Secret
	// PingTimeout bounds the startup check; default 5 seconds.
	PingTimeout time.Duration

	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// LoadConfig reads REDIS_URL.
func LoadConfig(l *config.Loader) Config {
	return Config{URL: l.Secret("REDIS_URL")}
}

// Open returns a client that has answered a ping.
func Open(ctx context.Context, cfg Config) (*redis.Client, error) {
	opts, err := redis.ParseURL(cfg.URL.Reveal())
	if err != nil {
		return nil, errors.New("redisx: cannot parse REDIS_URL")
	}
	client := redis.NewClient(opts)
	var traceOpts []redisotel.TracingOption
	if cfg.TracerProvider != nil {
		traceOpts = append(traceOpts, redisotel.WithTracerProvider(cfg.TracerProvider))
	}
	if err := redisotel.InstrumentTracing(client, traceOpts...); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisx: tracing: %w", err)
	}
	var metricOpts []redisotel.MetricsOption
	if cfg.MeterProvider != nil {
		metricOpts = append(metricOpts, redisotel.WithMeterProvider(cfg.MeterProvider))
	}
	if err := redisotel.InstrumentMetrics(client, metricOpts...); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisx: metrics: %w", err)
	}
	timeout := cfg.PingTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisx: ping: %w", err)
	}
	return client, nil
}
