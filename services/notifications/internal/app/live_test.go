package app_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
)

func newTestHub(t *testing.T, bus *fakes.FanoutBus, buffer int) (*app.Hub, *fakes.Fanout) {
	t.Helper()
	f := bus.Replica()
	hub, err := app.NewHub(f, buffer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.SetDeliver(hub.Deliver)
	return hub, f
}

func TestHubJoinsOnTheFirstSubscriberAndLeavesAfterTheLast(t *testing.T) {
	seed := testSeed(t)
	r := rand.New(rand.NewPCG(seed, 11))
	bus := &fakes.FanoutBus{}
	hub, fanout := newTestHub(t, bus, 4)
	users := []string{ada, grace, guest}
	var open []*app.Subscription
	var openUsers []string
	for range 300 {
		if len(open) > 0 && r.IntN(2) == 0 {
			i := r.IntN(len(open))
			open[i].Close()
			open[i].Close() // closing twice is harmless
			open, openUsers = slices.Delete(open, i, i+1), slices.Delete(openUsers, i, i+1)
		} else {
			u := users[r.IntN(len(users))]
			s, err := hub.Subscribe(context.Background(), u)
			if err != nil {
				t.Fatal(err)
			}
			open, openUsers = append(open, s), append(openUsers, u)
		}
		if err := hub.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		want := slices.Compact(slices.Sorted(slices.Values(openUsers)))
		if got := fanout.Joined(); !slices.Equal(got, want) {
			t.Fatalf("joined %v, want %v", got, want)
		}
	}
}

func TestSubscribersOnlyReceiveTheirOwnNotifications(t *testing.T) {
	bus := &fakes.FanoutBus{}
	hub, f := newTestHub(t, bus, 4)
	mine, _ := hub.Subscribe(context.Background(), ada)
	defer mine.Close()
	_ = f.Publish(context.Background(), domain.Notification{ID: "1", RecipientID: grace})
	_ = f.Publish(context.Background(), domain.Notification{ID: "2", RecipientID: ada})
	if n := <-mine.Notifications(); n.ID != "2" {
		t.Fatalf("got %s", n.ID)
	}
	select {
	case n := <-mine.Notifications():
		t.Fatalf("received someone else's %s", n.ID)
	default:
	}
}

func TestASlowSubscriberIsDroppedInsteadOfBlockingOrSkipping(t *testing.T) {
	bus := &fakes.FanoutBus{}
	hub, f := newTestHub(t, bus, 2)
	slow, _ := hub.Subscribe(context.Background(), ada)
	fast, _ := hub.Subscribe(context.Background(), ada)
	for i := range 3 {
		_ = f.Publish(context.Background(), domain.Notification{ID: string(rune('a' + i)), RecipientID: ada})
		<-fast.Notifications()
	}
	select {
	case <-slow.Done():
	default:
		t.Fatal("slow subscriber still open")
	}
	if !errors.Is(slow.Err(), app.ErrSlowSubscriber) {
		t.Fatalf("Err = %v", slow.Err())
	}
	if fast.Err() != nil {
		t.Fatal("fast subscriber affected")
	}
	if err := hub.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	fast.Close()
	if fast.Err() != nil || len(f.Joined()) != 0 {
		t.Fatalf("after close: err %v, joined %v", fast.Err(), f.Joined())
	}
}
