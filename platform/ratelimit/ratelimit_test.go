package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/platform/internal/testseed"
)

var epoch = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// harness drives one Limiter implementation with a controllable clock.
type harness struct {
	name    string
	limiter Limiter
	set     func(time.Time)
	check   func() error
}

func tokenBucketHarness() harness {
	c := &fakeClock{now: epoch}
	tb := NewTokenBucket(c)
	return harness{name: "TokenBucket", limiter: tb, set: func(t time.Time) { c.now = t }, check: tb.CheckInvariants}
}

func gcraHarness(t *testing.T) harness {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.SetTime(epoch)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return harness{
		name:    "RedisGCRA",
		limiter: NewRedisGCRA(client, "rl:test"),
		set: func(at time.Time) {
			mr.SetTime(at)
			// Expire keys whose TTL has passed, as real Redis would by now.
			mr.FastForward(0)
		},
		check: func() error { return nil },
	}
}

func harnesses(t *testing.T) []harness { return []harness{tokenBucketHarness(), gcraHarness(t)} }

// model is the reference: a token bucket in exact rational arithmetic,
// written for clarity, not speed.
type model struct {
	limit  Limit
	tokens *big.Rat // in events
	at     time.Time
}

func newModel(l Limit, at time.Time) *model {
	return &model{limit: l, tokens: new(big.Rat).SetInt64(l.Burst), at: at}
}

// ratDuration converts a rational number of nanoseconds, rounded up.
func ratDuration(r *big.Rat) time.Duration {
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return time.Duration(q.Int64())
}

func (m *model) allow(now time.Time, n int64) Decision {
	elapsed := new(big.Rat).SetFrac64(int64(now.Sub(m.at))*m.limit.Rate, int64(m.limit.Period))
	m.at = now
	m.tokens.Add(m.tokens, elapsed)
	burst := new(big.Rat).SetInt64(m.limit.Burst)
	if m.tokens.Cmp(burst) > 0 {
		m.tokens.Set(burst)
	}
	cost := new(big.Rat).SetInt64(n)
	perToken := new(big.Rat).SetFrac64(int64(m.limit.Period), m.limit.Rate) // ns per token

	d := Decision{Limit: m.limit.Burst}
	if m.tokens.Cmp(cost) >= 0 {
		m.tokens.Sub(m.tokens, cost)
		d.Allowed = true
	} else {
		short := new(big.Rat).Sub(cost, m.tokens)
		d.RetryAfter = ratDuration(short.Mul(short, perToken))
	}
	floor := new(big.Int).Quo(m.tokens.Num(), m.tokens.Denom())
	d.Remaining = floor.Int64()
	missing := new(big.Rat).Sub(burst, m.tokens)
	d.Reset = ratDuration(missing.Mul(missing, perToken))
	return d
}

// exactLimit returns a random limit whose emission interval is a whole
// number of microseconds, the case in which GCRA is exact.
func exactLimit(r *rand.Rand) Limit {
	rates := []int64{1, 2, 4, 5, 8, 10, 20, 25, 50, 100, 1000}
	periods := []time.Duration{10 * time.Millisecond, time.Second, time.Minute}
	for {
		l := Limit{Rate: rates[r.IntN(len(rates))], Period: periods[r.IntN(len(periods))], Burst: 1 + r.Int64N(20)}
		if l.Validate() == nil && l.Period.Microseconds()%l.Rate == 0 {
			return l
		}
	}
}

// arrivals returns a random, non-decreasing sequence of arrival times in
// whole microseconds, mixing same-instant bursts, short gaps and long idles.
func arrivals(r *rand.Rand, l Limit, count int) []time.Time {
	interval := l.Period / time.Duration(l.Rate)
	at := epoch
	out := make([]time.Time, count)
	for i := range out {
		switch r.IntN(4) {
		case 0: // same instant
		case 1:
			at = at.Add(time.Duration(r.Int64N(int64(interval)/1000+1)) * time.Microsecond)
		case 2:
			at = at.Add(time.Duration(r.Int64N(int64(interval)*3/1000+1)) * time.Microsecond)
		default:
			at = at.Add(time.Duration(r.Int64N(int64(l.Period)*int64(l.Burst)/1000+1)) * time.Microsecond)
		}
		out[i] = at
	}
	return out
}

type event struct {
	at   time.Time
	cost int64
}

func run(t *testing.T, h harness, l Limit, times []time.Time, costs []int64, key string) ([]Decision, []event) {
	t.Helper()
	var decisions []Decision
	var admitted []event
	for i, at := range times {
		h.set(at)
		d, err := h.limiter.Allow(context.Background(), key, l, costs[i])
		if err != nil {
			t.Fatalf("%s: Allow: %v", h.name, err)
		}
		if err := h.check(); err != nil {
			t.Fatalf("%s: after call %d: %v", h.name, i, err)
		}
		decisions = append(decisions, d)
		if d.Allowed {
			admitted = append(admitted, event{at: at, cost: costs[i]})
		}
	}
	return decisions, admitted
}

func randomCosts(r *rand.Rand, l Limit, count int) []int64 {
	out := make([]int64, count)
	for i := range out {
		out[i] = 1
		if r.IntN(4) == 0 {
			out[i] = 1 + r.Int64N(min(3, l.Burst))
		}
	}
	return out
}

func TestDecisionsMatchReferenceModel(t *testing.T) {
	r := testseed.Rand(testseed.Seed(t))
	for trial := range 40 {
		l := exactLimit(r)
		times := arrivals(r, l, 200)
		costs := randomCosts(r, l, len(times))
		key := fmt.Sprintf("k%d", trial)

		m := newModel(l, epoch)
		want := make([]Decision, len(times))
		for i, at := range times {
			want[i] = m.allow(at, costs[i])
		}
		for _, h := range harnesses(t) {
			got, _ := run(t, h, l, times, costs, key)
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%s trial %d limit %+v call %d at +%v cost %d:\n got %+v\nwant %+v",
						h.name, trial, l, i, times[i].Sub(epoch), costs[i], got[i], want[i])
				}
			}
		}
	}
}

func TestNoWindowAdmitsMoreThanBurstPlusRate(t *testing.T) {
	r := testseed.Rand(testseed.Seed(t))
	for trial := range 20 {
		l := exactLimit(r)
		times := arrivals(r, l, 300)
		costs := randomCosts(r, l, len(times))
		for _, h := range harnesses(t) {
			_, admitted := run(t, h, l, times, costs, fmt.Sprintf("w%d", trial))
			for i := range admitted {
				var sum int64
				for j := i; j < len(admitted); j++ {
					sum += admitted[j].cost
					window := int64(admitted[j].at.Sub(admitted[i].at))
					// sum <= burst + window*rate/period, in integers.
					if sum*int64(l.Period) > l.Burst*int64(l.Period)+window*l.Rate {
						t.Fatalf("%s limit %+v: %d admitted in a window of %v", h.name, l, sum, time.Duration(window))
					}
				}
			}
		}
	}
}

func TestBurstAtOneInstantIsCapped(t *testing.T) {
	l := PerSecond(10, 5)
	for _, h := range harnesses(t) {
		allowed := 0
		for range 50 {
			d, err := h.limiter.Allow(context.Background(), "instant", l, 1)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed {
				allowed++
			}
		}
		if allowed != 5 {
			t.Errorf("%s: %d allowed at one instant, want burst 5", h.name, allowed)
		}
	}
}

func TestLongRunRateConvergesUnderOverload(t *testing.T) {
	l := PerSecond(20, 10)
	const seconds = 60
	for _, h := range harnesses(t) {
		allowed := int64(0)
		// Four times the allowed rate.
		step := l.Period / time.Duration(l.Rate*4)
		for at := epoch; at.Before(epoch.Add(seconds * time.Second)); at = at.Add(step) {
			h.set(at)
			d, err := h.limiter.Allow(context.Background(), "overload", l, 1)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed {
				allowed++
			}
		}
		lower, upper := l.Rate*seconds, l.Rate*seconds+l.Burst
		if allowed < lower || allowed > upper {
			t.Errorf("%s: admitted %d in %ds, want within [%d, %d]", h.name, allowed, seconds, lower, upper)
		}
	}
}

func TestWaitingRetryAfterIsExactlyEnough(t *testing.T) {
	r := testseed.Rand(testseed.Seed(t))
	for range 20 {
		l := exactLimit(r)
		for _, h := range harnesses(t) {
			at := epoch
			h.set(at)
			key := fmt.Sprintf("retry-%d", r.Uint64())
			var denied Decision
			for {
				d, err := h.limiter.Allow(context.Background(), key, l, 1)
				if err != nil {
					t.Fatal(err)
				}
				if !d.Allowed {
					denied = d
					break
				}
			}
			if denied.RetryAfter <= 0 {
				t.Fatalf("%s: denied with RetryAfter %v", h.name, denied.RetryAfter)
			}
			if denied.RetryAfter > time.Microsecond {
				h.set(at.Add(denied.RetryAfter - time.Microsecond))
				if d, _ := h.limiter.Allow(context.Background(), key, l, 1); d.Allowed {
					t.Fatalf("%s %+v: allowed before RetryAfter %v", h.name, l, denied.RetryAfter)
				}
			}
			h.set(at.Add(denied.RetryAfter))
			if d, _ := h.limiter.Allow(context.Background(), key, l, 1); !d.Allowed {
				t.Fatalf("%s %+v: still denied after RetryAfter %v: %+v", h.name, l, denied.RetryAfter, d)
			}
		}
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l := PerMinute(1, 1)
	for _, h := range harnesses(t) {
		a, _ := h.limiter.Allow(context.Background(), "user:a", l, 1)
		b, _ := h.limiter.Allow(context.Background(), "user:b", l, 1)
		a2, _ := h.limiter.Allow(context.Background(), "user:a", l, 1)
		if !a.Allowed || !b.Allowed || a2.Allowed {
			t.Errorf("%s: a=%v b=%v a again=%v", h.name, a.Allowed, b.Allowed, a2.Allowed)
		}
	}
}

func TestInvalidLimitsAndCostsAreRejected(t *testing.T) {
	cases := map[string]struct {
		limit Limit
		cost  int64
		want  error
	}{
		"zero rate":            {Limit{Rate: 0, Period: time.Second, Burst: 1}, 1, ErrInvalidLimit},
		"negative period":      {Limit{Rate: 1, Period: -time.Second, Burst: 1}, 1, ErrInvalidLimit},
		"zero burst":           {Limit{Rate: 1, Period: time.Second, Burst: 0}, 1, ErrInvalidLimit},
		"overflowing burst":    {Limit{Rate: 1, Period: time.Hour, Burst: 1 << 40}, 1, ErrInvalidLimit},
		"sub-microsecond rate": {Limit{Rate: 2_000_000, Period: time.Second, Burst: 1}, 1, ErrInvalidLimit},
		"zero cost":            {PerSecond(1, 5), 0, ErrInvalidCost},
		"cost above burst":     {PerSecond(1, 5), 6, ErrInvalidCost},
	}
	for _, h := range harnesses(t) {
		for name, c := range cases {
			if _, err := h.limiter.Allow(context.Background(), "k", c.limit, c.cost); !errors.Is(err, c.want) {
				t.Errorf("%s %s: want %v, got %v", h.name, name, c.want, err)
			}
		}
	}
}

func TestIdleBucketsAreSwept(t *testing.T) {
	c := &fakeClock{now: epoch}
	tb := NewTokenBucket(c)
	l := PerSecond(1, 1)
	for i := range sweepEvery - 1 {
		_, _ = tb.Allow(context.Background(), fmt.Sprintf("k%d", i), l, 1)
	}
	if tb.Len() != sweepEvery-1 {
		t.Fatalf("Len = %d", tb.Len())
	}
	c.now = c.now.Add(2 * time.Second) // every bucket is full again
	_, _ = tb.Allow(context.Background(), "trigger", l, 1)
	if tb.Len() != 1 {
		t.Fatalf("after sweep Len = %d, want only the new key", tb.Len())
	}
	if err := tb.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestRedisKeysExpireWhenBucketIsFull(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	g := NewRedisGCRA(client, "rl:sync")
	if _, err := g.Allow(context.Background(), "u1", PerSecond(10, 10), 3); err != nil {
		t.Fatal(err)
	}
	ttl := mr.TTL("rl:sync:u1")
	if ttl <= 0 || ttl > 300*time.Millisecond {
		t.Fatalf("TTL = %v, want the 300ms it takes to refill 3 tokens", ttl)
	}
}
