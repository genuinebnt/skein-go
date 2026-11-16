package redisstore

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
	"github.com/genuinebnt/skein-go/services/gateway/internal/fakes"
	"github.com/genuinebnt/skein-go/services/gateway/internal/testseed"
)

type storeUnderTest struct {
	name    string
	store   app.IdempotencyStore
	advance func(time.Duration)
	check   func() error
}

func newMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

// stores returns the Redis adapter and the in-memory fake, so every
// contract test proves they behave the same.
func stores(t *testing.T) []storeUnderTest {
	mr, client := newMiniredis(t)
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	memory := fakes.NewIdempotencyStore(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	return []storeUnderTest{
		{"redis", NewIdempotencyStore(client, "gw"), mr.FastForward, func() error { return nil }},
		{"memory", memory, func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }, memory.CheckInvariants},
	}
}

func pending(fp, owner string) domain.IdempotencyRecord {
	return domain.IdempotencyRecord{State: domain.RecordPending, Fingerprint: fp, Owner: owner}
}

func complete(fp string, status int, body string) domain.IdempotencyRecord {
	return domain.IdempotencyRecord{State: domain.RecordComplete, Fingerprint: fp,
		Response: domain.StoredResponse{Status: status, ContentType: "application/json", Body: []byte(body)}}
}

func TestStoresReserveOnceAndReturnTheExistingRecord(t *testing.T) {
	ctx := context.Background()
	for _, s := range stores(t) {
		if _, reserved, err := s.store.Reserve(ctx, "u:k", pending("f", "o1"), time.Minute); err != nil || !reserved {
			t.Fatalf("%s: first reserve = %v, %v", s.name, reserved, err)
		}
		existing, reserved, err := s.store.Reserve(ctx, "u:k", pending("f", "o2"), time.Minute)
		if err != nil || reserved || existing.Owner != "o1" || existing.State != domain.RecordPending {
			t.Fatalf("%s: second reserve = %+v, %v, %v", s.name, existing, reserved, err)
		}
		if err := s.check(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoresCompleteOnlyForTheOwnerAndKeepTheResponse(t *testing.T) {
	ctx := context.Background()
	for _, s := range stores(t) {
		_, _, _ = s.store.Reserve(ctx, "u:k", pending("f", "o1"), time.Minute)
		if err := s.store.Complete(ctx, "u:k", "intruder", complete("f", 201, "{}"), time.Hour); !errors.Is(err, app.ErrNotOwner) {
			t.Errorf("%s: stranger completed: %v", s.name, err)
		}
		if err := s.store.Complete(ctx, "u:k", "o1", complete("f", 201, `{"id":"n1"}`), 24*time.Hour); err != nil {
			t.Fatalf("%s: complete: %v", s.name, err)
		}
		if err := s.check(); err != nil {
			t.Fatal(err)
		}
		// The pending TTL no longer applies; the 24 hour one does.
		s.advance(2 * time.Minute)
		existing, reserved, err := s.store.Reserve(ctx, "u:k", pending("f", "o2"), time.Minute)
		if err != nil || reserved || existing.State != domain.RecordComplete || string(existing.Response.Body) != `{"id":"n1"}` || existing.Response.Status != 201 {
			t.Fatalf("%s: after complete = %+v, %v, %v", s.name, existing, reserved, err)
		}
		if err := s.store.Release(ctx, "u:k", "o1"); !errors.Is(err, app.ErrNotOwner) {
			t.Errorf("%s: released a completed record: %v", s.name, err)
		}
		s.advance(24 * time.Hour)
		if _, reserved, _ := s.store.Reserve(ctx, "u:k", pending("f", "o3"), time.Minute); !reserved {
			t.Errorf("%s: record outlived its 24 hour TTL", s.name)
		}
	}
}

func TestStoresReleaseFreesTheKeyAndExpiredReservationsCannotBeCompleted(t *testing.T) {
	ctx := context.Background()
	for _, s := range stores(t) {
		_, _, _ = s.store.Reserve(ctx, "u:k", pending("f", "o1"), time.Minute)
		if err := s.store.Release(ctx, "u:k", "o1"); err != nil {
			t.Fatalf("%s: release: %v", s.name, err)
		}
		if _, reserved, _ := s.store.Reserve(ctx, "u:k", pending("f", "o2"), time.Minute); !reserved {
			t.Fatalf("%s: key not free after release", s.name)
		}
		// o2's reservation expires and o3 takes the key: o2 must not
		// overwrite o3's reservation.
		s.advance(time.Minute + time.Second)
		if _, reserved, _ := s.store.Reserve(ctx, "u:k", pending("f", "o3"), time.Minute); !reserved {
			t.Fatalf("%s: expired reservation still held", s.name)
		}
		if err := s.store.Complete(ctx, "u:k", "o2", complete("f", 200, "{}"), time.Hour); !errors.Is(err, app.ErrNotOwner) {
			t.Errorf("%s: late owner completed: %v", s.name, err)
		}
		if err := s.check(); err != nil {
			t.Fatal(err)
		}
	}
}

// A random sequence of operations against both stores gives the same
// results at every step.
func TestRedisStoreMatchesMemoryStoreUnderRandomOperations(t *testing.T) {
	rng := testseed.Rand(testseed.Seed(t))
	ctx := context.Background()
	ss := stores(t)
	owners := []string{"o1", "o2", "o3"}
	keys := []string{"u:a", "u:b"}
	for step := 0; step < 400; step++ {
		key, owner := keys[rng.IntN(len(keys))], owners[rng.IntN(len(owners))]
		op := rng.IntN(4)
		ttl := time.Duration(1+rng.IntN(3)) * time.Second
		var results [2]string
		for i, s := range ss {
			switch op {
			case 0:
				existing, reserved, err := s.store.Reserve(ctx, key, pending("f", owner), ttl)
				results[i] = describe(existing.State, existing.Owner, reserved, err)
			case 1:
				err := s.store.Complete(ctx, key, owner, complete("f", 200, "{}"), 10*time.Second)
				results[i] = describe("", "", false, err)
			case 2:
				err := s.store.Release(ctx, key, owner)
				results[i] = describe("", "", false, err)
			case 3:
				s.advance(time.Second)
			}
			if err := s.check(); err != nil {
				t.Fatal(err)
			}
		}
		if results[0] != results[1] {
			t.Fatalf("step %d op %d: redis %q, memory %q", step, op, results[0], results[1])
		}
	}
}

func describe(state domain.RecordState, owner string, reserved bool, err error) string {
	return string(state) + "|" + owner + "|" + map[bool]string{true: "reserved", false: "-"}[reserved] + "|" +
		map[bool]string{true: "not-owner", false: ""}[errors.Is(err, app.ErrNotOwner)]
}

func TestRedisStoreReportsUndecodableRecords(t *testing.T) {
	mr, client := newMiniredis(t)
	if err := mr.Set("gw:idem:u:k", "not json"); err != nil {
		t.Fatal(err)
	}
	store := NewIdempotencyStore(client, "gw")
	if _, _, err := store.Reserve(context.Background(), "u:k", pending("f", "o"), time.Minute); err == nil {
		t.Error("undecodable record was not reported")
	}
	if err := store.Release(context.Background(), "u:k", "o"); !errors.Is(err, app.ErrNotOwner) {
		t.Errorf("release of an undecodable record = %v", err)
	}
}

func TestRevocationBusReachesEverySubscriber(t *testing.T) {
	_, client := newMiniredis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewRevocationBus(client, "gw", slog.New(slog.DiscardHandler))
	got := make(chan domain.Revocation, 4)
	for range 2 {
		go func() {
			sub := NewRevocationBus(client, "gw", slog.New(slog.DiscardHandler))
			_ = sub.Subscribe(ctx, func(r domain.Revocation) { got <- r })
		}()
	}
	rev := domain.Revocation{UserID: "u1", NoteID: "n1", WorkspaceID: "w1"}
	deadline := time.Now().Add(5 * time.Second)
	// Subscriptions are asynchronous; publish until both have seen one.
	seen := 0
	for seen < 2 && time.Now().Before(deadline) {
		n, err := client.PubSubNumSub(ctx, "gw:access-revoked").Result()
		if err != nil {
			t.Fatal(err)
		}
		if n["gw:access-revoked"] < 2 {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if err := bus.Publish(ctx, rev); err != nil {
			t.Fatal(err)
		}
		for seen < 2 {
			select {
			case r := <-got:
				if r != rev {
					t.Fatalf("got %+v", r)
				}
				seen++
			case <-time.After(5 * time.Second):
				t.Fatal("broadcast not delivered")
			}
		}
	}
	if seen < 2 {
		t.Fatal("subscribers never registered")
	}
	// Malformed payloads are skipped, not fatal.
	if err := client.Publish(ctx, "gw:access-revoked", "garbage").Err(); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, domain.Revocation{UserID: "u2"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case r := <-got:
			if r.UserID != "u2" {
				t.Fatalf("got %+v after garbage", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("bus stopped after a malformed message")
		}
	}
}
