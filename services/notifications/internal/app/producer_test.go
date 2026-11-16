package app_test

import (
	"cmp"
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

func TestCommentNotifiesTheNotesCreatorAndMentionedReaders(t *testing.T) {
	h := newHarness(t)
	loc := domain.Location{NoteID: note, BlockID: "1:5", Start: 2, End: 9}
	h.comment(grace, time.Minute, app.CommentEvent{CommentID: "c1", NoteID: note, Mentions: []string{ada}, Body: "@ada look", Location: loc})

	got := recipientsOf(h.store.Notifications())
	slices.SortFunc(got, func(a, b recipientKind) int { return cmp.Compare(a.user, b.user) })
	want := []recipientKind{{ada, domain.KindMention}, {creator, domain.KindComment}}
	if !slices.Equal(got, want) {
		t.Fatalf("notified %v, want %v", got, want)
	}
	n := h.inboxOf(ada)[0]
	if n.Title != "Grace mentioned you in Raft log compaction" || n.Location != loc || n.ActorID != grace || n.Body != "@ada look" {
		t.Errorf("mention %+v", n)
	}
}

func TestMentioningSomeoneWhoCannotReadTheNoteSendsNothing(t *testing.T) {
	h := newHarness(t)
	h.comment(grace, time.Minute, app.CommentEvent{CommentID: "c1", NoteID: note, Mentions: []string{guest}, Body: "@gus"})
	if got := h.inboxOf(guest); len(got) != 0 {
		t.Fatalf("guest who cannot read the note was notified: %+v", got)
	}
	// And the other way: the same mention of a reader notifies them.
	h.comment(grace, 2*time.Minute, app.CommentEvent{CommentID: "c2", NoteID: note, Mentions: []string{ada}, Body: "@ada"})
	if got := h.inboxOf(ada); len(got) != 1 {
		t.Fatalf("reader not notified: %+v", got)
	}
}

func TestRedeliveredEventChangesNothing(t *testing.T) {
	h := newHarness(t)
	c := app.CommentEvent{CommentID: "c1", NoteID: note, Body: "hi"}
	ev := h.comment(grace, time.Minute, c)
	before := h.store.Notifications()
	h.applyEvent(func(ev app.Event) (app.Outcome, error) { return h.producer.Comment(context.Background(), ev, c) }, ev, app.OutcomeDuplicate)
	if after := h.store.Notifications(); !slices.EqualFunc(before, after, func(a, b domain.Notification) bool { return a == b }) {
		t.Fatalf("redelivery changed the store")
	}
}

func TestAFailedLookupLeavesTheEventUnprocessedForRetry(t *testing.T) {
	h := newHarness(t)
	h.authz.Err = errors.New("identity down")
	ev := h.event(grace, time.Minute)
	c := app.CommentEvent{CommentID: "c1", NoteID: note, Body: "hi"}
	if _, err := h.producer.Comment(context.Background(), ev, c); err == nil {
		t.Fatal("want an error while identity is down")
	}
	h.authz.Err = nil
	h.applyEvent(func(ev app.Event) (app.Outcome, error) { return h.producer.Comment(context.Background(), ev, c) }, ev, app.OutcomeApplied)
	if len(h.inboxOf(creator)) != 1 {
		t.Fatal("retry did not notify")
	}
}

func TestTenEditsMentioningTheSamePersonNotifyOnce(t *testing.T) {
	h := newHarness(t)
	text := "Draft: @ada please review"
	for i := range 10 {
		text += " more"
		h.blocks(grace, time.Duration(i)*time.Second, app.ChangedBlock{ID: "1:7", Kind: "paragraph", Text: text})
	}
	got := h.inboxOf(ada)
	if len(got) != 1 || got[0].Occurrences != 1 || got[0].Location.BlockID != "1:7" {
		t.Fatalf("inbox %+v, want one mention", got)
	}
}

func TestAMentionRetypedWithinTheWindowCoalescesAndAfterItNotifiesAgain(t *testing.T) {
	h := newHarness(t)
	block := func(text string, at time.Duration) {
		h.blocks(grace, at, app.ChangedBlock{ID: "1:7", Kind: "paragraph", Text: text})
	}
	block("@ada one", 0)
	block("removed", time.Second)
	block("@ada two", 2*time.Second)
	got := h.inboxOf(ada)
	if len(got) != 1 || got[0].Occurrences != 2 || got[0].Body != "@ada two" {
		t.Fatalf("inbox %+v, want one notification with two occurrences and the latest body", got)
	}
	block("gone", time.Hour)
	block("@ada three", time.Hour+time.Second)
	if got := h.inboxOf(ada); len(got) != 2 {
		t.Fatalf("a mention after the window should notify again: %+v", got)
	}
}

func TestReadNotificationsAreNotCoalescedInto(t *testing.T) {
	h := newHarness(t)
	h.blocks(grace, 0, app.ChangedBlock{ID: "1:1", Kind: "paragraph", Text: "@ada"})
	first := h.inboxOf(ada)[0]
	if err := h.inbox.MarkRead(context.Background(), ada, []string{first.ID}); err != nil {
		t.Fatal(err)
	}
	h.blocks(grace, time.Second, app.ChangedBlock{ID: "1:2", Kind: "paragraph", Text: "@ada again"})
	if got := h.inboxOf(ada); len(got) != 2 || h.unread(ada) != 1 {
		t.Fatalf("a read notification absorbed a new mention: %+v", got)
	}
}

func TestMentionsInCodeOfUnknownHandlesAndOfTheActorAreIgnored(t *testing.T) {
	h := newHarness(t)
	h.blocks(grace, 0,
		app.ChangedBlock{ID: "1:1", Kind: domain.BlockKindCode, Text: "// @ada"},
		app.ChangedBlock{ID: "1:2", Kind: "paragraph", Text: "@nobody and @grace and `@ada` and ada@example.com"},
	)
	if n := len(h.store.Notifications()); n != 0 {
		t.Fatalf("%d notifications, want none", n)
	}
}

func TestMentionOfANonReaderInBlockTextSendsNothing(t *testing.T) {
	h := newHarness(t)
	h.blocks(grace, 0, app.ChangedBlock{ID: "1:1", Kind: "paragraph", Text: "@gus have a look"})
	if n := len(h.store.Notifications()); n != 0 {
		t.Fatalf("guest notified")
	}
}

func TestRemovedBlocksForgetTheirMentions(t *testing.T) {
	h := newHarness(t)
	h.blocks(grace, 0, app.ChangedBlock{ID: "1:1", Kind: "paragraph", Text: "@ada"})
	h.mustApply(func(ev app.Event) (app.Outcome, error) {
		return h.producer.BlocksChanged(context.Background(), ev, app.BlocksChangedEvent{NoteID: note, RemovedBlockIDs: []string{"1:1"}})
	}, grace, time.Second)
	if err := h.inbox.MarkRead(context.Background(), ada, []string{h.inboxOf(ada)[0].ID}); err != nil {
		t.Fatal(err)
	}
	// The block came back (undo): a new mention, not one already known.
	h.blocks(grace, 2*time.Second, app.ChangedBlock{ID: "1:1", Kind: "paragraph", Text: "@ada"})
	if got := h.inboxOf(ada); len(got) != 2 {
		t.Fatalf("inbox %+v", got)
	}
}

func TestPreferencesDecideInboxAndEmail(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.inbox.UpdatePreferences(ctx, creator, []domain.Preference{{Kind: domain.KindComment, Email: true}}); err != nil {
		t.Fatal(err)
	}
	if err := h.inbox.UpdatePreferences(ctx, ada, []domain.Preference{{Kind: domain.KindMention}}); err != nil {
		t.Fatal(err)
	}
	h.comment(grace, time.Minute, app.CommentEvent{CommentID: "c1", NoteID: note, Mentions: []string{ada}, Body: "x"})
	if got := h.inboxOf(creator); len(got) != 0 {
		t.Errorf("in-app off, yet in the inbox: %+v", got)
	}
	if got := h.inboxOf(ada); len(got) != 0 {
		t.Errorf("both channels off, yet in the inbox: %+v", got)
	}
	jobs := h.store.Jobs()
	if len(jobs) != 1 || jobs[0].RecipientID != creator || h.wakes != 1 {
		t.Fatalf("email jobs %+v (wakes %d), want one for the creator", jobs, h.wakes)
	}
	if n := len(h.store.Notifications()); n != 1 {
		t.Errorf("%d notifications stored, want only the email-only one", n)
	}
}

func TestDefaultPreferencesEmailMentionsButNotComments(t *testing.T) {
	h := newHarness(t)
	h.comment(grace, time.Minute, app.CommentEvent{CommentID: "c1", NoteID: note, Mentions: []string{ada}, Body: "x"})
	jobs := h.store.Jobs()
	if len(jobs) != 1 || jobs[0].RecipientID != ada {
		t.Fatalf("jobs %+v, want one for ada's mention only", jobs)
	}
	if !jobs[0].DueAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("job due %v, want the digest delay after now", jobs[0].DueAt)
	}
}

func TestANewerFactFromTheSameSourceRefreshesTheNotification(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	access := func(offset time.Duration, want app.Outcome) {
		h.applyEvent(func(ev app.Event) (app.Outcome, error) {
			return h.producer.AccessChanged(ctx, ev, app.AccessChangedEvent{WorkspaceID: ws, NoteID: secret, UserID: ada})
		}, h.event(grace, offset), want)
	}
	access(time.Minute, app.OutcomeApplied)
	first := h.inboxOf(ada)[0]
	if err := h.inbox.MarkRead(ctx, ada, []string{first.ID}); err != nil {
		t.Fatal(err)
	}
	access(time.Hour, app.OutcomeApplied)
	got := h.inboxOf(ada)
	if len(got) != 1 || got[0].ID != first.ID || got[0].Read() || got[0].Occurrences != 2 || !got[0].CreatedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("refresh: %+v", got)
	}
	access(30*time.Minute, app.OutcomeApplied) // an older fact arriving late changes nothing
	if again := h.inboxOf(ada); again[0] != got[0] {
		t.Fatalf("stale fact changed the notification: %+v", again[0])
	}
}

func TestAccessChangeForAGuestIsAShareAndForAMemberAChange(t *testing.T) {
	h := newHarness(t)
	// Guests get no NOTE_CREATE on the workspace; give the guest a viewer
	// role on the note to see a share.
	h.authz.Decide = func(user string, c app.Check, _ time.Time) bool {
		if user == guest {
			return c.Action == app.ActionNoteRead && c.ResourceID == note
		}
		return c.Action != app.ActionNoteEdit
	}
	ctx := context.Background()
	for _, u := range []string{guest, ada} {
		h.mustApply(func(ev app.Event) (app.Outcome, error) {
			return h.producer.AccessChanged(ctx, ev, app.AccessChangedEvent{WorkspaceID: ws, NoteID: note, UserID: u})
		}, grace, time.Minute)
	}
	g, a := h.inboxOf(guest), h.inboxOf(ada)
	if len(g) != 1 || g[0].Kind != domain.KindShare || g[0].Title != "Grace shared Raft log compaction with you" || g[0].Body != "Role: viewer." {
		t.Errorf("guest: %+v", g)
	}
	if len(a) != 1 || a[0].Kind != domain.KindAccessChanged || a[0].Body != "Role: commenter." {
		t.Errorf("member: %+v", a)
	}
}

func TestAgentEventsMayOnlyNotifyTheRunsUser(t *testing.T) {
	h := newHarness(t)
	ev := h.event(ada, time.Minute)
	ev.ActorAgentRunID = "run-1"
	if _, err := h.producer.AgentEvent(context.Background(), ev, app.AgentEvent{RecipientID: grace, Title: "spam"}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("agent notified another user: %v", err)
	}
	plain := h.event(ada, time.Minute)
	if _, err := h.producer.AgentEvent(context.Background(), plain, app.AgentEvent{RecipientID: ada}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("event without a run accepted: %v", err)
	}
	h.applyEvent(func(app.Event) (app.Outcome, error) {
		return h.producer.AgentEvent(context.Background(), ev, app.AgentEvent{RecipientID: ada, Title: "The agent finished: weekly summary"})
	}, ev, app.OutcomeApplied)
	if got := h.inboxOf(ada); len(got) != 1 || got[0].Kind != domain.KindAgentRunFinished || got[0].Source != (domain.Source{Kind: domain.SourceAgentRun, ID: "run-1"}) {
		t.Fatalf("inbox %+v", got)
	}
}

func TestRequestsLocatedInAnUnreadableNoteAreDropped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.mustApply(func(ev app.Event) (app.Outcome, error) {
		return h.producer.Request(ctx, ev, domain.Request{RecipientID: grace, Kind: domain.KindComment, Title: "Salaries", Location: domain.Location{NoteID: secret}})
	}, ada, time.Minute)
	if got := h.inboxOf(grace); len(got) != 0 {
		t.Fatalf("leaked a note title: %+v", got)
	}
	ev := h.event(ada, time.Minute)
	if _, err := h.producer.Request(ctx, ev, domain.Request{RecipientID: "not-a-uuid", Kind: domain.KindComment, Title: "x"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("bad request: %v", err)
	}
}

func TestNotePurgeErasesItsNotificationsAndMentions(t *testing.T) {
	h := newHarness(t)
	h.comment(grace, time.Minute, app.CommentEvent{CommentID: "c1", NoteID: note, Mentions: []string{ada}, Body: "quoted text",
		Location: domain.Location{NoteID: note, BlockID: "1:1"}})
	h.blocks(grace, 2*time.Minute, app.ChangedBlock{ID: "1:2", Kind: "paragraph", Text: "@ada"})
	if h.unread(ada) == 0 {
		t.Fatal("setup: nothing to purge")
	}
	h.mustApply(func(ev app.Event) (app.Outcome, error) { return h.producer.NotePurged(context.Background(), ev, note) }, creator, time.Hour)
	if n := len(h.store.Notifications()); n != 0 || h.unread(ada) != 0 || len(h.store.Jobs()) != 0 {
		t.Fatalf("after purge: %d notifications, %d unread, %d jobs", n, h.unread(ada), len(h.store.Jobs()))
	}
}

func TestNewNotificationsInvalidateTheCachedUnreadCount(t *testing.T) {
	h := newHarness(t)
	if h.unread(ada) != 0 {
		t.Fatal("setup")
	}
	h.blocks(grace, 0, app.ChangedBlock{ID: "1:1", Kind: "paragraph", Text: "@ada"})
	if got := h.unread(ada); got != 1 {
		t.Fatalf("unread %d after a new notification, want 1 (stale cache)", got)
	}
	loads := h.cache.Loads()
	_ = h.unread(ada)
	if h.cache.Loads() != loads {
		t.Error("second read did not hit the cache")
	}
}

func TestNewNotificationsArePushedToSubscribersOnAnyReplica(t *testing.T) {
	h := newHarness(t)
	other := h.bus.Replica()
	otherHub, err := app.NewHub(other, 4, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	other.SetDeliver(otherHub.Deliver)
	sub, err := otherHub.Subscribe(context.Background(), ada)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	h.blocks(grace, 0, app.ChangedBlock{ID: "1:1", Kind: "paragraph", Text: "@ada"})
	select {
	case n := <-sub.Notifications():
		if n.RecipientID != ada || n.Kind != domain.KindMention {
			t.Fatalf("pushed %+v", n)
		}
	default:
		t.Fatal("nothing pushed to the other replica")
	}
	if err := otherHub.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestPushFailureDoesNotFailTheEvent(t *testing.T) {
	h := newHarness(t)
	h.bus.PublishErr = errors.New("redis down")
	h.blocks(grace, 0, app.ChangedBlock{ID: "1:1", Kind: "paragraph", Text: "@ada"})
	if len(h.inboxOf(ada)) != 1 {
		t.Fatal("notification lost when the push failed")
	}
}

// TestDuplicatedDeliveryGivesTheSameInboxAsExactlyOnce replays a random
// stream of events with random redeliveries (at-least-once, as Pub/Sub does
// after a relay crash) and compares the result with delivering each event
// once.
func TestDuplicatedDeliveryGivesTheSameInboxAsExactlyOnce(t *testing.T) {
	seed := testSeed(t)
	r := rand.New(rand.NewPCG(seed, 9))
	type step struct {
		actor string
		apply func(h *harness, ev app.Event) (app.Outcome, error)
	}
	users := []string{ada, grace, guest, creator}
	handles := []string{"@ada", "@grace", "@gus", "@cleo", "@nobody", ""}
	var steps []step
	for i := range 60 {
		actor := users[r.IntN(len(users))]
		switch r.IntN(3) {
		case 0:
			text := handles[r.IntN(len(handles))] + " " + handles[r.IntN(len(handles))]
			block := []string{"1:1", "1:2", "1:3"}[r.IntN(3)]
			steps = append(steps, step{actor, func(h *harness, ev app.Event) (app.Outcome, error) {
				return h.producer.BlocksChanged(context.Background(), ev, app.BlocksChangedEvent{NoteID: note,
					Blocks: []app.ChangedBlock{{ID: block, Kind: "paragraph", Text: text}}})
			}})
		case 1:
			mention := users[r.IntN(len(users))]
			id := "c" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			steps = append(steps, step{actor, func(h *harness, ev app.Event) (app.Outcome, error) {
				return h.producer.Comment(context.Background(), ev, app.CommentEvent{CommentID: id, NoteID: note, Mentions: []string{mention}, Body: "x"})
			}})
		default:
			target := users[r.IntN(len(users))]
			steps = append(steps, step{actor, func(h *harness, ev app.Event) (app.Outcome, error) {
				return h.producer.AccessChanged(context.Background(), ev, app.AccessChangedEvent{WorkspaceID: ws, NoteID: note, UserID: target})
			}})
		}
	}
	run := func(duplicates bool) []domain.Notification {
		h := newHarness(t)
		var events []app.Event
		for i, s := range steps {
			ev := h.event(s.actor, time.Duration(i)*time.Second)
			events = append(events, ev)
			h.applyEvent(func(ev app.Event) (app.Outcome, error) { return s.apply(h, ev) }, ev, app.OutcomeApplied)
			if duplicates && r.IntN(3) == 0 {
				j := r.IntN(i + 1) // redeliver this or an earlier event
				h.applyEvent(func(ev app.Event) (app.Outcome, error) { return steps[j].apply(h, ev) }, events[j], app.OutcomeDuplicate)
			}
		}
		return h.store.Notifications()
	}
	once, dup := run(false), run(true)
	if !slices.Equal(once, dup) {
		t.Fatalf("redeliveries changed the result: %d vs %d notifications", len(once), len(dup))
	}
	if len(once) == 0 {
		t.Fatal("the stream notified nobody; the test proves nothing")
	}
}

func TestEventsWithoutIDsAreRejected(t *testing.T) {
	h := newHarness(t)
	_, err := h.producer.Comment(context.Background(), app.Event{Consumer: "x", OccurredAt: t0}, app.CommentEvent{CommentID: "c", NoteID: note})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("got %v", err)
	}
}
