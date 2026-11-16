package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

const ada, grace = "01930000-0000-7000-8000-000000000001", "01930000-0000-7000-8000-000000000002"

func TestUnreadCountIsLoadedOnceUntilInvalidated(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	for name, store := range map[string]redisx.Store{
		"memory": redisx.NewMemoryStore(time.Now),
		"redis":  redisx.NewRedisStore(client, Namespace),
	} {
		t.Run(name, func(t *testing.T) {
			cache, err := NewUnreadCache(store, time.Minute, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			loads, count := 0, uint32(3)
			load := func(context.Context) (uint32, error) { loads++; return count, nil }
			for range 3 {
				if n, err := cache.Get(ctx, ada, load); err != nil || n != 3 {
					t.Fatalf("Get = %d, %v", n, err)
				}
			}
			count = 5
			if err := cache.Invalidate(ctx, ada); err != nil {
				t.Fatal(err)
			}
			if n, _ := cache.Get(ctx, ada, load); n != 5 || loads != 2 {
				t.Fatalf("after invalidation: %d with %d loads", n, loads)
			}
			if n, _ := cache.Get(ctx, grace, load); n != 5 || loads != 3 {
				t.Fatal("users share an entry")
			}
		})
	}
}

func TestUnreadCountFailsOpenWhenRedisIsDown(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	cache, err := NewUnreadCache(redisx.NewRedisStore(client, Namespace), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	mr.Close()
	n, err := cache.Get(context.Background(), ada, func(context.Context) (uint32, error) { return 7, nil })
	if err != nil || n != 7 {
		t.Fatalf("Get with Redis down = %d, %v; want the loaded count", n, err)
	}
}

func TestLiveMessagesRoundTrip(t *testing.T) {
	n := domain.Notification{ID: "01936f60-5e6f-7a80-9b92-0d1e2f304152", RecipientID: ada, Kind: domain.KindMention, Title: "t", Body: "b",
		Location: domain.Location{NoteID: grace, BlockID: "1:2", Start: 1, End: 4}, ActorID: grace, Occurrences: 1, InInbox: true,
		CreatedAt: time.Date(2026, 11, 16, 9, 30, 0, 123000, time.UTC)}
	n.UpdatedAt = n.CreatedAt
	data, err := EncodeLive(n)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLive(data)
	if err != nil || got != n {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	if _, err := DecodeLive([]byte(`{"v":2,"id":"x"}`)); err == nil {
		t.Error("future version accepted")
	}
}

// TestFanoutReachesOnlyReplicasThatJoinedTheUser runs two replicas' hubs
// on one (in-process) Redis.
func TestFanoutReachesOnlyReplicasThatJoinedTheUser(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newReplica := func() (*Fanout, *app.Hub) {
		client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		f := NewFanout(ctx, client, nil)
		hub, err := app.NewHub(f, 8, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.SetDeliver(hub.Deliver)
		go func() { _ = f.Run(ctx) }()
		return f, hub
	}
	a, _ := newReplica()
	_, hubB := newReplica()
	sub, err := hubB.Subscribe(ctx, ada)
	if err != nil {
		t.Fatal(err)
	}
	waitSubscribed(t, mr, channel(ada))

	n := domain.Notification{ID: "01936f60-5e6f-7a80-9b92-0d1e2f304152", RecipientID: ada, Kind: domain.KindShare, Title: "shared", CreatedAt: time.Now().UTC()}
	if err := a.Publish(ctx, domain.Notification{ID: "x", RecipientID: grace, Title: "not for ada"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Publish(ctx, n); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-sub.Notifications():
		if got.ID != n.ID || got.Title != "shared" {
			t.Fatalf("got %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live notification across replicas")
	}
	sub.Close()
	waitSubscribed(t, mr, "")
	if err := hubB.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

// waitSubscribed waits until Redis has exactly the given channel
// subscribed, or none when channel is empty.
func waitSubscribed(t *testing.T, mr *miniredis.Miniredis, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		chans := mr.PubSubChannels("")
		if (want == "" && len(chans) == 0) || (len(chans) == 1 && chans[0] == want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("channels %v, want %q", mr.PubSubChannels(""), want)
}

func TestPublishReportsRedisFailures(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	f := NewFanout(context.Background(), client, nil)
	mr.Close()
	if err := f.Publish(context.Background(), domain.Notification{RecipientID: ada}); err == nil {
		t.Fatal("publish to a dead Redis succeeded")
	}
}
