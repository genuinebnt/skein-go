package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// sweepEvery is how many Allow calls pass between sweeps of idle buckets.
// A sweep is O(keys), so this keeps its amortised cost constant.
const sweepEvery = 1024

// bucket is one key's state. level counts tokens scaled by the period in
// nanoseconds: a full bucket holds Burst*Period, an event of cost n takes
// n*Period, and every nanosecond adds Rate.
type bucket struct {
	limit Limit
	level int64
	at    time.Time
}

func (b *bucket) capacity() int64 { return b.limit.Burst * int64(b.limit.Period) }

// refill brings level up to date at now without overflowing.
func (b *bucket) refill(now time.Time) {
	elapsed := int64(now.Sub(b.at))
	if elapsed <= 0 {
		return
	}
	b.at = now
	missing := b.capacity() - b.level
	if elapsed >= missing/b.limit.Rate+1 {
		b.level = b.capacity()
		return
	}
	b.level += elapsed * b.limit.Rate
	if b.level > b.capacity() {
		b.level = b.capacity()
	}
}

// ceilDiv returns ceil(a/b) for a >= 0, b > 0.
func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// TokenBucket is an in-process Limiter. It holds one bucket per key and
// drops buckets that have refilled completely, since a full bucket is the
// same as no bucket.
type TokenBucket struct {
	clock Clock

	mu      sync.Mutex
	buckets map[string]*bucket
	calls   int
}

// NewTokenBucket returns an empty limiter reading time from clock
// (SystemClock when nil).
func NewTokenBucket(clock Clock) *TokenBucket {
	if clock == nil {
		clock = SystemClock
	}
	return &TokenBucket{clock: clock, buckets: make(map[string]*bucket)}
}

// Allow implements Limiter. A key used with a new limit starts again from
// a full bucket under that limit; keys should map to one limit.
func (tb *TokenBucket) Allow(_ context.Context, key string, limit Limit, n int64) (Decision, error) {
	if err := limit.validateCost(n); err != nil {
		return Decision{}, err
	}
	now := tb.clock.Now()

	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.calls++
	if tb.calls%sweepEvery == 0 {
		tb.sweepLocked(now)
	}

	b, ok := tb.buckets[key]
	if !ok || b.limit != limit {
		b = &bucket{limit: limit, at: now}
		b.level = b.capacity()
		tb.buckets[key] = b
	}
	b.refill(now)

	period := int64(limit.Period)
	cost := n * period
	d := Decision{Limit: limit.Burst}
	if b.level >= cost {
		b.level -= cost
		d.Allowed = true
	} else {
		d.RetryAfter = time.Duration(ceilDiv(cost-b.level, limit.Rate))
	}
	d.Remaining = b.level / period
	d.Reset = time.Duration(ceilDiv(b.capacity()-b.level, limit.Rate))
	return d, nil
}

func (tb *TokenBucket) sweepLocked(now time.Time) {
	for key, b := range tb.buckets {
		b.refill(now)
		if b.level == b.capacity() {
			delete(tb.buckets, key)
		}
	}
}

// Len returns the number of keys with a bucket that is not known to be
// full.
func (tb *TokenBucket) Len() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return len(tb.buckets)
}

// CheckInvariants verifies that every bucket's level is within
// [0, capacity] and its limit is valid.
func (tb *TokenBucket) CheckInvariants() error {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	for key, b := range tb.buckets {
		if err := b.limit.Validate(); err != nil {
			return fmt.Errorf("ratelimit: invariant: bucket %q: %w", key, err)
		}
		if b.level < 0 || b.level > b.capacity() {
			return fmt.Errorf("ratelimit: invariant: bucket %q level %d outside [0, %d]", key, b.level, b.capacity())
		}
	}
	return nil
}
