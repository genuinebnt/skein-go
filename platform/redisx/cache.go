package redisx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"
)

// ErrInvalidCache means New was given unusable options.
var ErrInvalidCache = errors.New("redisx: invalid cache options")

// Codec turns values into bytes and back.
type Codec[V any] interface {
	Marshal(V) ([]byte, error)
	Unmarshal([]byte) (V, error)
}

// JSONCodec encodes values as JSON, the default.
type JSONCodec[V any] struct{}

// Marshal implements Codec.
func (JSONCodec[V]) Marshal(v V) ([]byte, error) { return json.Marshal(v) }

// Unmarshal implements Codec.
func (JSONCodec[V]) Unmarshal(data []byte) (V, error) {
	var v V
	err := json.Unmarshal(data, &v)
	return v, err
}

// Options configure New.
type Options[V any] struct {
	// Name scopes keys ("readable-notes") and labels metrics. Letters,
	// digits, '-' and '_' only, so it cannot forge another cache's prefix.
	Name string
	TTL  time.Duration
	// LoadTimeout bounds a GetOrLoad loader; default 5 seconds.
	LoadTimeout   time.Duration
	Codec         Codec[V] // JSONCodec when nil
	MeterProvider metric.MeterProvider
}

// Cache is a typed cache over a Store.
type Cache[V any] struct {
	store       Store
	name        string
	ttl         time.Duration
	loadTimeout time.Duration
	codec       Codec[V]
	group       singleflight.Group
	metrics     *cacheMetrics
	attrs       metric.MeasurementOption
}

type cacheMetrics struct {
	hits, misses, invalidations, errors metric.Int64Counter
}

func newCacheMetrics(mp metric.MeterProvider) (*cacheMetrics, error) {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	meter := mp.Meter("github.com/genuinebnt/skein-go/platform/redisx")
	var m cacheMetrics
	var errs [4]error
	m.hits, errs[0] = meter.Int64Counter("skein.cache.hits", metric.WithDescription("Cache lookups that found an entry."))
	m.misses, errs[1] = meter.Int64Counter("skein.cache.misses", metric.WithDescription("Cache lookups that found nothing."))
	m.invalidations, errs[2] = meter.Int64Counter("skein.cache.invalidations", metric.WithDescription("Cache entries removed by Delete or InvalidateTags."))
	m.errors, errs[3] = meter.Int64Counter("skein.cache.errors", metric.WithDescription("Cache store or codec failures."))
	return &m, errors.Join(errs[:]...)
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// New returns a cache on store.
func New[V any](store Store, opts Options[V]) (*Cache[V], error) {
	if !validName(opts.Name) || opts.TTL <= 0 {
		return nil, fmt.Errorf("%w: name %q ttl %v", ErrInvalidCache, opts.Name, opts.TTL)
	}
	if opts.Codec == nil {
		opts.Codec = JSONCodec[V]{}
	}
	if opts.LoadTimeout <= 0 {
		opts.LoadTimeout = 5 * time.Second
	}
	m, err := newCacheMetrics(opts.MeterProvider)
	if err != nil {
		return nil, fmt.Errorf("redisx: metrics: %w", err)
	}
	return &Cache[V]{
		store: store, name: opts.Name, ttl: opts.TTL, loadTimeout: opts.LoadTimeout,
		codec: opts.Codec, metrics: m,
		attrs: metric.WithAttributes(attribute.String("cache.name", opts.Name)),
	}, nil
}

func (c *Cache[V]) key(k string) string { return c.name + ":" + k }

func (c *Cache[V]) countError(ctx context.Context, operation string) {
	c.metrics.errors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("cache.name", c.name), attribute.String("operation", operation)))
}

// Get returns the cached value for key.
func (c *Cache[V]) Get(ctx context.Context, key string) (V, bool, error) {
	var zero V
	data, found, err := c.store.Get(ctx, c.key(key))
	if err != nil {
		c.countError(ctx, "get")
		return zero, false, fmt.Errorf("redisx: get %s: %w", c.name, err)
	}
	if !found {
		c.metrics.misses.Add(ctx, 1, c.attrs)
		return zero, false, nil
	}
	v, err := c.codec.Unmarshal(data)
	if err != nil {
		// Usually a changed type after a deploy: treat as a miss so the
		// next load overwrites it.
		c.countError(ctx, "decode")
		c.metrics.misses.Add(ctx, 1, c.attrs)
		return zero, false, nil
	}
	c.metrics.hits.Add(ctx, 1, c.attrs)
	return v, true, nil
}

// Set stores v under key for the cache's TTL, with tags.
func (c *Cache[V]) Set(ctx context.Context, key string, v V, tags ...string) error {
	data, err := c.codec.Marshal(v)
	if err != nil {
		c.countError(ctx, "encode")
		return fmt.Errorf("redisx: encode %s: %w", c.name, err)
	}
	if err := c.store.Set(ctx, c.key(key), data, c.ttl, tags...); err != nil {
		c.countError(ctx, "set")
		return fmt.Errorf("redisx: set %s: %w", c.name, err)
	}
	return nil
}

// Loader computes a value on a miss, and the tags to store it with.
type Loader[V any] func(ctx context.Context) (V, []string, error)

// GetOrLoad returns the cached value, or loads, stores and returns it.
// Concurrent misses on one key share one load. Store failures fall back to
// the loader; loader errors are returned and nothing is cached.
func (c *Cache[V]) GetOrLoad(ctx context.Context, key string, load Loader[V]) (V, error) {
	if v, found, err := c.Get(ctx, key); err == nil && found {
		return v, nil
	}
	results := c.group.DoChan(key, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
		defer cancel()
		v, tags, err := load(loadCtx)
		if err != nil {
			return nil, err
		}
		// A failed write still returns the loaded value; Set counted it.
		_ = c.Set(loadCtx, key, v, tags...)
		return v, nil
	})
	select {
	case r := <-results:
		if r.Err != nil {
			var zero V
			return zero, r.Err
		}
		return r.Val.(V), nil
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	}
}

// Delete drops keys from this cache.
func (c *Cache[V]) Delete(ctx context.Context, keys ...string) error {
	scoped := make([]string, len(keys))
	for i, k := range keys {
		scoped[i] = c.key(k)
	}
	n, err := c.store.Delete(ctx, scoped...)
	if err != nil {
		c.countError(ctx, "delete")
		return fmt.Errorf("redisx: delete %s: %w", c.name, err)
	}
	c.metrics.invalidations.Add(ctx, int64(n), c.attrs)
	return nil
}

// Invalidator drops tagged entries from every cache on a store. Event
// consumers hold one: a NoteRenamed event calls
// InvalidateTags(ctx, "note:<id>") whatever caches hold that note.
type Invalidator struct {
	store   Store
	metrics *cacheMetrics
	attrs   metric.MeasurementOption
}

// NewInvalidator returns an Invalidator for store.
func NewInvalidator(store Store, mp metric.MeterProvider) (*Invalidator, error) {
	m, err := newCacheMetrics(mp)
	if err != nil {
		return nil, fmt.Errorf("redisx: metrics: %w", err)
	}
	return &Invalidator{store: store, metrics: m, attrs: metric.WithAttributes(attribute.String("cache.name", "*"))}, nil
}

// InvalidateTags removes every entry carrying any of tags and returns how
// many were removed.
func (inv *Invalidator) InvalidateTags(ctx context.Context, tags ...string) (int, error) {
	n, err := inv.store.InvalidateTags(ctx, tags...)
	if err != nil {
		inv.metrics.errors.Add(ctx, 1, metric.WithAttributes(
			attribute.String("cache.name", "*"), attribute.String("operation", "invalidate_tags")))
		return 0, fmt.Errorf("redisx: invalidate tags %s: %w", strings.Join(tags, ","), err)
	}
	inv.metrics.invalidations.Add(ctx, int64(n), inv.attrs)
	return n, nil
}
