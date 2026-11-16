// Package redis adapts platform/redisx to identity's DecisionCache port.
// Entries are booleans under the "authorize" cache name with the decision
// TTL, tagged with the user, note and workspace they depend on; the store's
// tag invalidation (redisx records each entry's tags in the entry itself)
// drops them when access changes. In tests the same adapter runs over
// redisx.MemoryStore.
package redis

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/platform/redisx"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
)

// DecisionCache implements app.DecisionCache.
type DecisionCache struct {
	cache *redisx.Cache[bool]
	inv   *redisx.Invalidator
}

var _ app.DecisionCache = (*DecisionCache)(nil)

// NewDecisionCache returns a cache of Authorize decisions on store.
func NewDecisionCache(store redisx.Store, ttl time.Duration, mp metric.MeterProvider) (*DecisionCache, error) {
	c, err := redisx.New[bool](store, redisx.Options[bool]{Name: "authorize", TTL: ttl, MeterProvider: mp})
	if err != nil {
		return nil, err
	}
	inv, err := redisx.NewInvalidator(store, mp)
	if err != nil {
		return nil, err
	}
	return &DecisionCache{cache: c, inv: inv}, nil
}

// Get implements app.DecisionCache.
func (d *DecisionCache) Get(ctx context.Context, key string) (bool, bool, error) {
	return d.cache.Get(ctx, key)
}

// Set implements app.DecisionCache.
func (d *DecisionCache) Set(ctx context.Context, key string, allowed bool, tags ...string) error {
	return d.cache.Set(ctx, key, allowed, tags...)
}

// InvalidateTags implements app.DecisionCache.
func (d *DecisionCache) InvalidateTags(ctx context.Context, tags ...string) error {
	_, err := d.inv.InvalidateTags(ctx, tags...)
	return err
}
