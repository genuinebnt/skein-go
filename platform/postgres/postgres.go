package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/genuinebnt/skein-go/platform/config"
)

// Config configures Open. Zero values take pgx's defaults, which can also
// be set in the URL (pool_max_conns=...).
type Config struct {
	URL             config.Secret
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	// PingTimeout bounds the startup check; default 5 seconds.
	PingTimeout time.Duration

	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// LoadConfig reads DATABASE_URL and the optional DATABASE_MAX_CONNS.
func LoadConfig(l *config.Loader) Config {
	return Config{
		URL:      l.Secret("DATABASE_URL"),
		MaxConns: int32(l.IntRange("DATABASE_MAX_CONNS", 0, 0, 1000)),
	}
}

// Open returns a pool that has answered a ping.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL.Reveal())
	if err != nil {
		// pgx's parse errors can quote the URL, password included.
		return nil, errors.New("postgres: cannot parse DATABASE_URL")
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pc.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pc.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pc.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	var tracerOpts []otelpgx.Option
	if cfg.TracerProvider != nil {
		tracerOpts = append(tracerOpts, otelpgx.WithTracerProvider(cfg.TracerProvider))
	}
	pc.ConnConfig.Tracer = otelpgx.NewTracer(tracerOpts...)

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	var statsOpts []otelpgx.StatsOption
	if cfg.MeterProvider != nil {
		statsOpts = append(statsOpts, otelpgx.WithStatsMeterProvider(cfg.MeterProvider))
	}
	if err := otelpgx.RecordStats(pool, statsOpts...); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: pool metrics: %w", err)
	}
	timeout := cfg.PingTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// Beginner starts transactions. *pgxpool.Pool and *pgx.Conn implement it.
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Execer runs a statement. pgx.Tx, *pgxpool.Pool and *pgx.Conn implement
// it; outbox.Enqueue takes one so it can only be called with whatever the
// caller's transaction is.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// WithTx runs fn in a read-write transaction with the default isolation.
func WithTx(ctx context.Context, db Beginner, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return WithTxOptions(ctx, db, pgx.TxOptions{}, fn)
}

// WithTxOptions runs fn in a transaction with opts. It commits when fn
// returns nil, and rolls back when fn returns an error or panics.
func WithTxOptions(ctx context.Context, db Beginner, opts pgx.TxOptions, fn func(ctx context.Context, tx pgx.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			// The rollback error is lost here: the panic matters more.
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()
	if err := fn(ctx, tx); err != nil {
		// WithoutCancel: a cancelled request must still release its locks
		// now, not when the connection is eventually reaped.
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("postgres: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}
