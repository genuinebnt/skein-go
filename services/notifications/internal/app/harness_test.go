package app_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
)

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

func uid(n int) string { return fmt.Sprintf("01930000-0000-7000-8000-%012x", n) }

// The cast of these tests.
var (
	ada     = uid(1) // can read everything
	grace   = uid(2) // can read everything
	guest   = uid(3) // can read nothing
	creator = uid(4)
	ws      = uid(100)
	note    = uid(200)
	secret  = uid(201) // a note only ada can read
)

func testSeed(t *testing.T) uint64 {
	t.Helper()
	seed := rand.Uint64()
	if s := os.Getenv("SKEIN_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("SKEIN_SEED: %v", err)
		}
		seed = v
	}
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	return seed
}

type harness struct {
	t        *testing.T
	store    *fakes.Store
	authz    *fakes.Authorizer
	dir      *fakes.Directory
	cache    *fakes.UnreadCache
	bus      *fakes.FanoutBus
	fanout   *fakes.Fanout
	hub      *app.Hub
	clock    *fakes.Clock
	producer *app.Producer
	inbox    *app.Inbox
	events   int
	wakes    int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, store: fakes.NewStore(), cache: fakes.NewUnreadCache(), bus: &fakes.FanoutBus{}, clock: fakes.NewClock(t0)}
	h.authz = &fakes.Authorizer{Decide: func(user string, c app.Check, _ time.Time) bool {
		switch {
		case user == guest:
			return false
		case c.ResourceID == secret:
			return user == ada
		}
		return true
	}}
	h.dir = fakes.NewDirectory(
		fakes.DirectoryUser{Profile: app.Profile{UserID: ada, DisplayName: "Ada", Email: "ada@example.com"}, Handle: "ada", Workspaces: []string{ws}},
		fakes.DirectoryUser{Profile: app.Profile{UserID: grace, DisplayName: "Grace", Email: "grace@example.com"}, Handle: "grace", Workspaces: []string{ws}},
		fakes.DirectoryUser{Profile: app.Profile{UserID: guest, DisplayName: "Gus", Email: "gus@example.com"}, Handle: "gus", Workspaces: []string{ws}},
		fakes.DirectoryUser{Profile: app.Profile{UserID: creator, DisplayName: "Cleo", Email: "cleo@example.com"}, Handle: "cleo", Workspaces: []string{ws}},
	)
	h.fanout = h.bus.Replica()
	var err error
	h.hub, err = app.NewHub(h.fanout, 4, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.fanout.SetDeliver(h.hub.Deliver)
	h.producer, err = app.NewProducer(app.ProducerConfig{
		Store: h.store, Authorizer: h.authz, Directory: h.dir, UnreadCache: h.cache, Fanout: h.fanout,
		Clock: h.clock.Now, NewID: fakes.IDs(1), EmailEnqueued: func() { h.wakes++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.inbox = app.NewInbox(h.store, h.cache, h.clock.Now, nil)
	h.mustApply(func(ev app.Event) (app.Outcome, error) {
		return h.producer.NoteCreated(context.Background(), ev, note, ws, "Raft log compaction")
	}, creator, 0)
	h.mustApply(func(ev app.Event) (app.Outcome, error) {
		return h.producer.NoteCreated(context.Background(), ev, secret, ws, "Salaries")
	}, ada, 0)
	return h
}

// event returns a fresh event by actor, at offset after t0.
func (h *harness) event(actor string, offset time.Duration) app.Event {
	h.events++
	return app.Event{Consumer: "test", ID: uid(10000 + h.events), OccurredAt: t0.Add(offset), ActorUserID: actor}
}

func (h *harness) mustApply(f func(app.Event) (app.Outcome, error), actor string, offset time.Duration) app.Event {
	h.t.Helper()
	ev := h.event(actor, offset)
	h.applyEvent(f, ev, app.OutcomeApplied)
	return ev
}

func (h *harness) applyEvent(f func(app.Event) (app.Outcome, error), ev app.Event, want app.Outcome) {
	h.t.Helper()
	got, err := f(ev)
	if err != nil {
		h.t.Fatalf("event %s: %v", ev.ID, err)
	}
	if got != want {
		h.t.Fatalf("event %s: outcome %s, want %s", ev.ID, got, want)
	}
	h.check()
}

func (h *harness) check() {
	h.t.Helper()
	if err := h.store.CheckInvariants(); err != nil {
		h.t.Fatal(err)
	}
	if err := h.hub.CheckInvariants(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) inboxOf(user string) []domain.Notification {
	h.t.Helper()
	page, err := h.inbox.ListInbox(context.Background(), user, 500, "", false)
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Notifications
}

func (h *harness) unread(user string) uint32 {
	h.t.Helper()
	n, err := h.inbox.UnreadCount(context.Background(), user)
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) blocks(actor string, offset time.Duration, blocks ...app.ChangedBlock) app.Event {
	h.t.Helper()
	return h.mustApply(func(ev app.Event) (app.Outcome, error) {
		return h.producer.BlocksChanged(context.Background(), ev, app.BlocksChangedEvent{NoteID: note, Blocks: blocks})
	}, actor, offset)
}

func (h *harness) comment(actor string, offset time.Duration, c app.CommentEvent) app.Event {
	h.t.Helper()
	return h.mustApply(func(ev app.Event) (app.Outcome, error) { return h.producer.Comment(context.Background(), ev, c) }, actor, offset)
}

type recipientKind struct {
	user string
	kind domain.Kind
}

func recipientsOf(ns []domain.Notification) []recipientKind {
	var out []recipientKind
	for _, n := range ns {
		out = append(out, recipientKind{n.RecipientID, n.Kind})
	}
	return out
}
