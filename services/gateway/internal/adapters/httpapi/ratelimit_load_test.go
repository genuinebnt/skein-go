package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
	"github.com/genuinebnt/skein-go/services/gateway/internal/testseed"
)

// A load-shaped run through the whole gateway with the production limiter
// (GCRA in Redis; miniredis runs the same Lua, and its clock is simulated):
// two users each send five times their budget for six simulated minutes,
// at jittered arrival times. GCRA admits exactly burst + rate x elapsed
// over any span that starts from idle, and the rate per minute after the
// burst is spent converges to the budget.
func TestRateLimiterLongRunRateMatchesTheBudget(t *testing.T) {
	rng := testseed.Rand(testseed.Seed(t))
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const (
		rate   = 60 // per minute
		burst  = 10
		offer  = 5 // requests per second per user: five times the budget
		window = 6 * time.Minute
	)
	limits := generousLimits()
	limits[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectUser}] = ratelimit.PerMinute(rate, burst)
	s := newStack(t, stackOptions{limits: limits, limiter: ratelimit.NewRedisGCRA(client, "gw:rl")})
	handler := s.server.Handler()
	users := []string{"ada", "grace"}
	tokens := map[string]string{}
	for _, u := range users {
		tokens[u] = s.token(u, time.Hour)
	}

	start := time.Unix(1_700_000_000, 0)
	admitted := map[string]int{}
	perMinute := map[string][]int{}
	for _, u := range users {
		perMinute[u] = make([]int, int(window/time.Minute))
	}
	step := time.Second / offer
	for elapsed := time.Duration(0); elapsed < window; elapsed += step {
		for _, u := range users {
			// Jitter inside the step keeps arrivals irregular without
			// changing the offered rate.
			at := elapsed + time.Duration(rng.Int64N(int64(step)))
			mr.SetTime(start.Add(at))
			req := httptest.NewRequest(http.MethodGet, "/v1/notes/n1", nil)
			req.Header.Set("Authorization", "Bearer "+tokens[u])
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			switch rec.Code {
			case http.StatusOK:
				admitted[u]++
				perMinute[u][int(at/time.Minute)]++
			case http.StatusTooManyRequests:
			default:
				t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
			}
		}
	}

	want := burst + rate*int(window/time.Minute)
	for _, u := range users {
		t.Logf("%s: admitted %d of %d offered; per minute %v", u, admitted[u], offer*int(window/time.Second), perMinute[u])
		// The last arrival is up to one step before the window ends, so
		// GCRA may not yet have admitted the final emission.
		if admitted[u] < want-1 || admitted[u] > want {
			t.Errorf("%s: admitted %d, want %d (burst %d + %d/min for %s)", u, admitted[u], want, burst, rate, window)
		}
		for minute, n := range perMinute[u][1:] {
			if n < rate-1 || n > rate+1 {
				t.Errorf("%s: minute %d admitted %d, want %d±1", u, minute+1, n, rate)
			}
		}
		if first := perMinute[u][0]; first < rate+burst-1 || first > rate+burst {
			t.Errorf("%s: first minute admitted %d, want about %d (rate plus the burst)", u, first, rate+burst)
		}
	}
}
