//go:build integration

package redisstore

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
	"github.com/genuinebnt/skein-go/services/gateway/internal/testredis"
)

// These run the gateway's Redis adapters against a real server. The unit
// tests use miniredis, which runs Lua in gopher-lua with its own cjson and
// simulates expiry; here the owner-guard script, real key expiry, real
// pub/sub between two connections and the GCRA script shared by two
// replicas are exercised for real.

func TestRealRedisReservesAKeyForExactlyOneOfManyConcurrentRequests(t *testing.T) {
	client, namespace := testredis.Open(t)
	store := NewIdempotencyStore(client, namespace)
	ctx := context.Background()
	const requests = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners, seen := []string{}, map[string]int{}
	for i := range requests {
		owner := "o" + string(rune('A'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			existing, reserved, err := store.Reserve(ctx, "u:k", pending("f", owner), time.Minute)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				t.Error(err)
			case reserved:
				winners = append(winners, owner)
			default:
				seen[existing.Owner]++
			}
		}()
	}
	wg.Wait()
	if len(winners) != 1 || seen[winners[0]] != requests-1 {
		t.Fatalf("winners %v, losers saw %v", winners, seen)
	}
}

func TestRealRedisCompletesOnlyForTheOwnerAndReplaysTheResponse(t *testing.T) {
	client, namespace := testredis.Open(t)
	store := NewIdempotencyStore(client, namespace)
	ctx := context.Background()
	if _, reserved, err := store.Reserve(ctx, "u:k", pending("f", "o1"), time.Minute); err != nil || !reserved {
		t.Fatalf("reserve: %v %v", reserved, err)
	}
	if err := store.Complete(ctx, "u:k", "o2", complete("f", 200, `{"x":1}`), time.Minute); !errors.Is(err, app.ErrNotOwner) {
		t.Fatalf("a stranger completed the record: %v", err)
	}
	if err := store.Complete(ctx, "u:k", "o1", complete("f", 201, `{"x":1}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	existing, reserved, err := store.Reserve(ctx, "u:k", pending("f", "o3"), time.Minute)
	if err != nil || reserved || existing.State != domain.RecordComplete || existing.Response.Status != 201 || string(existing.Response.Body) != `{"x":1}` {
		t.Fatalf("replay: %+v reserved=%v err=%v", existing, reserved, err)
	}
	if err := store.Release(ctx, "u:k", "o1"); !errors.Is(err, app.ErrNotOwner) {
		t.Errorf("a completed record was released: %v", err)
	}
}

func TestRealRedisExpiresAbandonedReservations(t *testing.T) {
	client, namespace := testredis.Open(t)
	store := NewIdempotencyStore(client, namespace)
	ctx := context.Background()
	if _, reserved, _ := store.Reserve(ctx, "u:k", pending("f", "o1"), 100*time.Millisecond); !reserved {
		t.Fatal("not reserved")
	}
	time.Sleep(250 * time.Millisecond)
	if err := store.Complete(ctx, "u:k", "o1", complete("f", 200, "{}"), time.Minute); !errors.Is(err, app.ErrNotOwner) {
		t.Fatalf("completed an expired reservation: %v", err)
	}
	if _, reserved, err := store.Reserve(ctx, "u:k", pending("f", "o2"), time.Minute); err != nil || !reserved {
		t.Fatalf("the key was not free after expiry: %v %v", reserved, err)
	}
	if err := store.Release(ctx, "u:k", "o2"); err != nil {
		t.Fatal(err)
	}
	if n, _ := client.Exists(ctx, store.key("u:k")).Result(); n != 0 {
		t.Error("release left the key behind")
	}
}

func TestRealRedisGuardRefusesUndecodableRecords(t *testing.T) {
	client, namespace := testredis.Open(t)
	store := NewIdempotencyStore(client, namespace)
	ctx := context.Background()
	if err := client.Set(ctx, store.key("u:k"), "not json", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	// Redis's own cjson raises on bad input; the script must catch it.
	if err := store.Release(ctx, "u:k", "o"); !errors.Is(err, app.ErrNotOwner) {
		t.Errorf("release of an undecodable record = %v", err)
	}
	if _, _, err := store.Reserve(ctx, "u:k", pending("f", "o"), time.Minute); err == nil {
		t.Error("an undecodable record was not reported")
	}
}

func TestRealRedisBroadcastsRevocationsToAnotherReplica(t *testing.T) {
	client, namespace := testredis.Open(t)
	other := testredis.Client(t, testredis.URL(client))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	discard := slog.New(slog.DiscardHandler)
	got := make(chan domain.Revocation, 1)
	go func() {
		_ = NewRevocationBus(other, namespace, discard).Subscribe(ctx, func(r domain.Revocation) { got <- r })
	}()
	channel := namespace + ":access-revoked"
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if n, _ := client.PubSubNumSub(ctx, channel).Result(); n[channel] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no subscriber on %s", channel)
		}
	}
	rev := domain.Revocation{UserID: "u1", NoteID: "n1", WorkspaceID: "w1"}
	if err := NewRevocationBus(client, namespace, discard).Publish(ctx, rev); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r != rev {
			t.Errorf("got %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the other replica never received the revocation")
	}
}

func TestRealRedisRateLimitIsOneBudgetAcrossReplicas(t *testing.T) {
	client, namespace := testredis.Open(t)
	other := testredis.Client(t, testredis.URL(client))
	limits := domain.DefaultLimits()
	limits[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectUser}] = ratelimit.PerSecond(1, 3)
	limits[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectIP}] = ratelimit.PerSecond(100, 1000)
	discard := slog.New(slog.DiscardHandler)
	// The prefix main.go uses, under the test's namespace.
	replicas := make([]*app.Throttler, 2)
	for i, c := range []ratelimit.Limiter{ratelimit.NewRedisGCRA(client, namespace+":rl"), ratelimit.NewRedisGCRA(other, namespace+":rl")} {
		throttler, err := app.NewThrottler(c, limits, discard, nil)
		if err != nil {
			t.Fatal(err)
		}
		replicas[i] = throttler
	}
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.7")
	for i := range 3 {
		if v := replicas[i%2].Check(ctx, domain.BudgetAPI, "ada", ip); v.Limited() || !v.Checked {
			t.Fatalf("request %d refused: %+v", i, v)
		}
	}
	denied := replicas[1].Check(ctx, domain.BudgetAPI, "ada", ip)
	if !denied.Limited() || denied.Key.Subject != domain.SubjectUser || denied.Decision.RetryAfter <= 0 {
		t.Fatalf("the fourth request across both replicas was not limited: %+v", denied)
	}
	if v := replicas[0].Check(ctx, domain.BudgetAPI, "grace", ip); v.Limited() {
		t.Errorf("another user shares ada's budget: %+v", v)
	}
	if v := replicas[0].Check(ctx, domain.BudgetSearch, "ada", ip); v.Limited() {
		t.Errorf("the search budget shares the api budget: %+v", v)
	}
	time.Sleep(denied.Decision.RetryAfter + 50*time.Millisecond)
	if v := replicas[0].Check(ctx, domain.BudgetAPI, "ada", ip); v.Limited() {
		t.Errorf("still limited after Retry-After: %+v", v)
	}
}
