package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/platform/events"
)

// RelayConfig configures NewRelay. Zero values take the defaults.
type RelayConfig struct {
	BatchSize      int           // 100
	Concurrency    int           // ordering-key groups published at once; 16
	PollInterval   time.Duration // 500ms
	PublishTimeout time.Duration // per message; 10s
	InitialBackoff time.Duration // 1s
	MaxBackoff     time.Duration // 5m
	// Jitter maps a backoff to the delay actually used. The default picks
	// uniformly between half and all of it, so rows that failed together do
	// not retry together.
	Jitter func(time.Duration) time.Duration
	// Retention keeps published rows for inspection before Prune deletes
	// them; 0 means 7 days, negative disables pruning.
	Retention     time.Duration
	PruneInterval time.Duration // 1h

	Logger        *slog.Logger
	MeterProvider metric.MeterProvider
	Now           func() time.Time // for the lag metric; time.Now
}

func (c *RelayConfig) defaults() {
	setDefault(&c.BatchSize, 100)
	setDefault(&c.Concurrency, 16)
	setDefault(&c.PollInterval, 500*time.Millisecond)
	setDefault(&c.PublishTimeout, 10*time.Second)
	setDefault(&c.InitialBackoff, time.Second)
	setDefault(&c.MaxBackoff, 5*time.Minute)
	setDefault(&c.Retention, 7*24*time.Hour)
	setDefault(&c.PruneInterval, time.Hour)
	if c.Jitter == nil {
		c.Jitter = func(d time.Duration) time.Duration { return d/2 + rand.N(d/2+1) }
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.MeterProvider == nil {
		c.MeterProvider = otel.GetMeterProvider()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

func setDefault[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
	}
}

// Relay publishes outbox rows. Run one per service process.
type Relay struct {
	store   Store
	pub     events.Publisher
	cfg     RelayConfig
	wake    chan struct{}
	metrics relayMetrics
}

type relayMetrics struct {
	published, failures metric.Int64Counter
	lag                 metric.Float64Histogram
}

// NewRelay returns a relay moving rows from store to pub.
func NewRelay(store Store, pub events.Publisher, cfg RelayConfig) (*Relay, error) {
	cfg.defaults()
	meter := cfg.MeterProvider.Meter("github.com/genuinebnt/skein-go/platform/outbox")
	var m relayMetrics
	var errs [3]error
	m.published, errs[0] = meter.Int64Counter("skein.outbox.published", metric.WithDescription("Outbox rows published."))
	m.failures, errs[1] = meter.Int64Counter("skein.outbox.publish_failures", metric.WithDescription("Outbox publish attempts that failed."))
	m.lag, errs[2] = meter.Float64Histogram("skein.outbox.publish_lag", metric.WithUnit("s"),
		metric.WithDescription("Time from enqueue to publish."))
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("outbox: metrics: %w", err)
	}
	return &Relay{store: store, pub: pub, cfg: cfg, wake: make(chan struct{}, 1), metrics: m}, nil
}

// Notify wakes Run early, for example right after a transaction that
// enqueued events commits. It never blocks.
func (r *Relay) Notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Backoff returns the delay before retry number attempt (1 for the first
// retry), before jitter.
func (r *Relay) Backoff(attempt int32) time.Duration {
	d := r.cfg.InitialBackoff
	for i := int32(1); i < attempt && d < r.cfg.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, r.cfg.MaxBackoff)
}

// Result reports one RunOnce.
type Result struct {
	Claimed, Published, Failed, Skipped int
}

// outcome of publishing one record.
type outcome struct {
	attempted bool
	err       error
}

// RunOnce claims, publishes and records one batch.
func (r *Relay) RunOnce(ctx context.Context) (Result, error) {
	batch, err := r.store.Claim(ctx, r.cfg.BatchSize)
	if err != nil {
		return Result{}, fmt.Errorf("outbox: claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = batch.Rollback(context.WithoutCancel(ctx))
		}
	}()

	records := batch.Records()
	outcomes := r.publishAll(ctx, records)

	// Record outcomes even if ctx was cancelled during publishing, so an
	// ordinary shutdown does not turn published rows into duplicates.
	recordCtx := context.WithoutCancel(ctx)
	res := Result{Claimed: len(records)}
	var published []int64
	for i, rec := range records {
		o := outcomes[i]
		topic := metric.WithAttributes(attribute.String("messaging.destination.name", rec.Topic))
		switch {
		case !o.attempted:
			res.Skipped++
		case o.err == nil:
			res.Published++
			published = append(published, rec.ID)
			r.metrics.published.Add(recordCtx, 1, topic)
			r.metrics.lag.Record(recordCtx, r.cfg.Now().Sub(rec.CreatedAt).Seconds(), topic)
		case ctx.Err() != nil:
			// Interrupted by shutdown, not a broker failure: leave the row
			// as it was.
			res.Skipped++
		default:
			res.Failed++
			r.metrics.failures.Add(recordCtx, 1, topic)
			delay := r.cfg.Jitter(r.Backoff(rec.Attempts + 1))
			if err := batch.MarkFailed(recordCtx, rec.ID, delay, o.err.Error()); err != nil {
				return res, fmt.Errorf("outbox: mark failed: %w", err)
			}
			r.cfg.Logger.WarnContext(recordCtx, "outbox publish failed",
				slog.Int64("outbox_id", rec.ID), slog.String("event_id", rec.Envelope.ID),
				slog.String("topic", rec.Topic), slog.Int("attempt", int(rec.Attempts)+1),
				slog.Duration("retry_in", delay), slog.String("error", o.err.Error()))
		}
	}
	if len(published) > 0 {
		if err := batch.MarkPublished(recordCtx, published); err != nil {
			return res, fmt.Errorf("outbox: mark published: %w", err)
		}
	}
	if err := batch.Commit(recordCtx); err != nil {
		return res, fmt.Errorf("outbox: commit: %w", err)
	}
	committed = true
	return res, nil
}

// publishAll publishes records grouped by ordering key: groups run
// concurrently, each group in id order, and a group stops at its first
// failure so later events of that key are not published ahead of it.
func (r *Relay) publishAll(ctx context.Context, records []Record) []outcome {
	outcomes := make([]outcome, len(records))
	var groups [][]int
	byKey := map[string]int{}
	for i, rec := range records {
		key := rec.Envelope.OrderingKey
		if key == "" {
			groups = append(groups, []int{i})
			continue
		}
		g, ok := byKey[key]
		if !ok {
			g = len(groups)
			byKey[key] = g
			groups = append(groups, nil)
		}
		groups[g] = append(groups[g], i)
	}

	sem := make(chan struct{}, r.cfg.Concurrency)
	var wg sync.WaitGroup
	for _, group := range groups {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			for _, i := range group {
				if ctx.Err() != nil {
					return
				}
				pubCtx, cancel := context.WithTimeout(ctx, r.cfg.PublishTimeout)
				err := r.pub.Publish(pubCtx, records[i].Topic, records[i].Envelope)
				cancel()
				outcomes[i] = outcome{attempted: true, err: err}
				if err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	return outcomes
}

// Run relays until ctx is cancelled, then returns nil. Errors from a batch
// are logged and retried after PollInterval; they never stop the loop,
// because the relay must outlive a database failover.
func (r *Relay) Run(ctx context.Context) error {
	pruneTicker := time.NewTicker(r.cfg.PruneInterval)
	defer pruneTicker.Stop()
	for {
		res, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.cfg.Logger.ErrorContext(ctx, "outbox relay batch failed", slog.String("error", err.Error()))
		}
		if err == nil && res.Claimed == r.cfg.BatchSize {
			if ctx.Err() != nil {
				return nil
			}
			continue // more rows are probably waiting
		}
		timer := time.NewTimer(r.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-r.wake:
		case <-timer.C:
		case <-pruneTicker.C:
			r.prune(ctx)
		}
		timer.Stop()
	}
}

func (r *Relay) prune(ctx context.Context) {
	pruner, ok := r.store.(Pruner)
	if !ok || r.cfg.Retention < 0 {
		return
	}
	n, err := pruner.Prune(ctx, r.cfg.Retention)
	if err != nil {
		r.cfg.Logger.WarnContext(ctx, "outbox prune failed", slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		r.cfg.Logger.InfoContext(ctx, "outbox pruned published rows", slog.Int64("rows", n))
	}
}
