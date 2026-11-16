package ratelimit_test

import (
	"context"
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/platform/ratelimit"
)

type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }

func Example() {
	clock := &fixedClock{t: time.Unix(0, 0)}
	// In the gateway this would be ratelimit.NewRedisGCRA(client, "rl:search");
	// both implementations make the same decisions.
	var limiter ratelimit.Limiter = ratelimit.NewTokenBucket(clock)
	limit := ratelimit.PerMinute(60, 3) // one per second on average, bursts of 3

	for i := range 4 {
		d, _ := limiter.Allow(context.Background(), "user:0193", limit, 1)
		fmt.Printf("request %d: allowed=%v remaining=%d retry_after=%v\n", i+1, d.Allowed, d.Remaining, d.RetryAfter)
	}
	// Output:
	// request 1: allowed=true remaining=2 retry_after=0s
	// request 2: allowed=true remaining=1 retry_after=0s
	// request 3: allowed=true remaining=0 retry_after=0s
	// request 4: allowed=false remaining=0 retry_after=1s
}
