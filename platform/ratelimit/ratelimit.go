package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// Errors returned by this package. Test with errors.Is.
var (
	// ErrInvalidLimit means a Limit has a non-positive field or values too
	// large to compute with.
	ErrInvalidLimit = errors.New("ratelimit: invalid limit")
	// ErrInvalidCost means a cost is below 1 or above the burst, so the
	// request could never be allowed.
	ErrInvalidCost = errors.New("ratelimit: invalid cost")
)

// Limit allows Rate events per Period on average, with bursts of Burst.
type Limit struct {
	Rate   int64
	Period time.Duration
	Burst  int64
}

// PerSecond, PerMinute and PerHour build common limits.
func PerSecond(rate, burst int64) Limit { return Limit{Rate: rate, Period: time.Second, Burst: burst} }

// PerMinute returns a limit of rate events per minute.
func PerMinute(rate, burst int64) Limit { return Limit{Rate: rate, Period: time.Minute, Burst: burst} }

// PerHour returns a limit of rate events per hour.
func PerHour(rate, burst int64) Limit { return Limit{Rate: rate, Period: time.Hour, Burst: burst} }

// Validate reports whether the limit can be enforced exactly by both
// implementations.
func (l Limit) Validate() error {
	if l.Rate <= 0 || l.Period <= 0 || l.Burst <= 0 {
		return fmt.Errorf("%w: rate, period and burst must be positive: %+v", ErrInvalidLimit, l)
	}
	// TokenBucket stores Burst*Period nanoseconds in an int64.
	if l.Burst > math.MaxInt64/int64(l.Period) {
		return fmt.Errorf("%w: burst*period overflows: %+v", ErrInvalidLimit, l)
	}
	// GCRA needs an emission interval of at least one microsecond.
	if l.Period.Microseconds()/l.Rate < 1 {
		return fmt.Errorf("%w: more than one event per microsecond: %+v", ErrInvalidLimit, l)
	}
	return nil
}

func (l Limit) validateCost(n int64) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if n < 1 || n > l.Burst {
		return fmt.Errorf("%w: cost %d with burst %d", ErrInvalidCost, n, l.Burst)
	}
	return nil
}

// Decision is the outcome of one Allow call.
type Decision struct {
	Allowed bool
	// Limit is the burst: the most requests of cost 1 allowed at once.
	Limit int64
	// Remaining is how many more requests of cost 1 would be allowed right
	// now, after this one.
	Remaining int64
	// Reset is the time until the bucket is completely full again.
	Reset time.Duration
	// RetryAfter is zero when Allowed. Otherwise it is the wait after which
	// the same request would be allowed, if nothing else arrives first.
	RetryAfter time.Duration
}

// Limiter decides whether an event of cost n under limit may proceed for
// key. Implementations are safe for concurrent use. Errors mean the limiter
// itself failed (for example Redis is down); callers choose to fail open or
// closed.
type Limiter interface {
	Allow(ctx context.Context, key string, limit Limit, n int64) (Decision, error)
}

// Clock tells the time. TokenBucket uses it so tests can drive time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock is the real clock.
var SystemClock Clock = systemClock{}
