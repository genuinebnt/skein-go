package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/fakes"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

var ctx = context.Background()

func TestAuthCacheServesRepeatsAndForgetsAUserOnInvalidation(t *testing.T) {
	inner := fakes.NewAuthorizer()
	inner.SetRole("w", "n1", "bob", fakes.Editor)
	cache, err := NewAuthCache(inner, redisx.NewMemoryStore(time.Now), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bob := domain.Subject{UserID: "bob"}
	checks := []ports.Check{{Action: ports.ActionRead, Resource: "n1"}, {Action: ports.ActionEdit, Resource: "n1"}}
	for range 3 {
		got, err := cache.Authorize(ctx, bob, checks)
		if err != nil || !got[0] || !got[1] {
			t.Fatalf("%v %v", got, err)
		}
	}
	if inner.Calls() != 1 {
		t.Fatalf("identity called %d times, want 1", inner.Calls())
	}
	inner.SetRole("w", "n1", "bob", fakes.NoAccess)
	if got, _ := cache.Authorize(ctx, bob, checks); !got[0] {
		t.Fatal("within the TTL the cached decision stands until invalidated")
	}
	if err := cache.InvalidateUser(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if got, _ := cache.Authorize(ctx, bob, checks); got[0] || got[1] {
		t.Fatalf("after AccessChanged the revocation must apply: %v", got)
	}
}

func TestNoteCacheRoundTripsAndDeletes(t *testing.T) {
	c, err := NewNoteCache(redisx.NewMemoryStore(time.Now), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	n := domain.Note{ID: "n1", WorkspaceID: "w", Title: "Raft", TrashedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c.Set(ctx, n)
	if got, ok := c.Get(ctx, "n1"); !ok || got != n {
		t.Fatalf("%+v %v", got, ok)
	}
	c.Delete(ctx, "n1")
	if _, ok := c.Get(ctx, "n1"); ok {
		t.Fatal("deleted entry still served")
	}
}

func TestPresenceExpiresAndIsOrderedByReplica(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p := NewPresence(client, func() time.Time { return now })
	for _, r := range []crdt.ReplicaID{1<<40 + 3, 1<<40 + 1, 1<<40 + 2} {
		if err := p.Put(ctx, "n1", ports.Presence{UserID: "u", Replica: r, Anchor: "1:1"}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Remove(ctx, "n1", 1<<40+2); err != nil {
		t.Fatal(err)
	}
	got, err := p.List(ctx, "n1")
	if err != nil || len(got) != 2 || got[0].Replica != 1<<40+1 || got[1].Replica != 1<<40+3 || got[0].Anchor != "1:1" {
		t.Fatalf("%+v %v", got, err)
	}
	now = now.Add(2 * time.Minute)
	if got, _ := p.List(ctx, "n1"); len(got) != 0 {
		t.Fatalf("expired cursors listed: %+v", got)
	}
}
