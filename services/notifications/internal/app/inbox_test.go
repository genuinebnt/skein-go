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

// seedInbox gives ada n notifications with colliding timestamps, some read.
func seedInbox(t *testing.T, h *harness, r *rand.Rand, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		offset := time.Duration(r.IntN(n/3+1)) * time.Second // many equal times: the ID breaks ties
		h.mustApply(func(ev app.Event) (app.Outcome, error) {
			return h.producer.Request(ctx, ev, domain.Request{RecipientID: ada, Kind: domain.KindComment, Title: "n" + string(rune('A'+i%26)) + string(rune('a'+i/26))})
		}, grace, offset)
	}
	var toRead []string
	for _, x := range h.store.Notifications() {
		if r.IntN(3) == 0 {
			toRead = append(toRead, x.ID)
		}
	}
	if len(toRead) > 0 {
		if err := h.inbox.MarkRead(ctx, ada, toRead); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPagingThroughTheInboxReturnsEveryItemOnceNewestFirst(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 5))
	h := newHarness(t)
	seedInbox(t, h, r, 80)
	for _, unreadOnly := range []bool{false, true} {
		var oracle []domain.Notification
		for _, n := range h.store.Notifications() {
			if n.RecipientID == ada && (!unreadOnly || !n.Read()) {
				oracle = append(oracle, n)
			}
		}
		slices.SortFunc(oracle, func(a, b domain.Notification) int {
			return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
		})
		var got []domain.Notification
		token := ""
		size := uint32(1 + r.IntN(9))
		for pages := 0; ; pages++ {
			page, err := h.inbox.ListInbox(context.Background(), ada, size, token, unreadOnly)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Notifications) > int(size) {
				t.Fatalf("page of %d for size %d", len(page.Notifications), size)
			}
			wantUnread := uint32(0)
			for _, n := range oracle {
				if !n.Read() {
					wantUnread++
				}
			}
			if unreadOnly && page.Unread != uint32(len(oracle)) || !unreadOnly && page.Unread != wantUnread {
				t.Fatalf("unread badge %d", page.Unread)
			}
			got = append(got, page.Notifications...)
			if page.NextPageToken == "" {
				break
			}
			token = page.NextPageToken
			if pages > 200 {
				t.Fatal("paging does not end")
			}
		}
		if !slices.Equal(got, oracle) {
			t.Fatalf("unreadOnly=%t: paged %d items, oracle %d (size %d)", unreadOnly, len(got), len(oracle), size)
		}
	}
}

func TestNotificationsCreatedWhilePagingDoNotShiftLaterPages(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 6))
	h := newHarness(t)
	seedInbox(t, h, r, 20)
	first, err := h.inbox.ListInbox(context.Background(), ada, 5, "", false)
	if err != nil {
		t.Fatal(err)
	}
	h.mustApply(func(ev app.Event) (app.Outcome, error) {
		return h.producer.Request(context.Background(), ev, domain.Request{RecipientID: ada, Kind: domain.KindShare, Title: "newest"})
	}, grace, time.Hour)
	second, err := h.inbox.ListInbox(context.Background(), ada, 5, first.NextPageToken, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range second.Notifications {
		if n.Title == "newest" || slices.Contains(first.Notifications, n) {
			t.Fatalf("second page repeats or includes a newer item: %+v", n)
		}
	}
}

func TestAPageTokenReusedWithAnotherFilterIsRejected(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 7))
	h := newHarness(t)
	seedInbox(t, h, r, 10)
	page, err := h.inbox.ListInbox(context.Background(), ada, 2, "", false)
	if err != nil || page.NextPageToken == "" {
		t.Fatalf("setup: %v", err)
	}
	if _, err := h.inbox.ListInbox(context.Background(), ada, 2, page.NextPageToken, true); !app.IsInvalidArgument(err) {
		t.Fatalf("got %v", err)
	}
	if _, err := h.inbox.ListInbox(context.Background(), ada, 2, "garbage", false); !app.IsInvalidArgument(err) {
		t.Fatalf("garbage token: %v", err)
	}
}

func TestMarkReadOnlyTouchesTheCallersNotificationsAndIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.comment(grace, time.Minute, app.CommentEvent{CommentID: "c1", NoteID: note, Mentions: []string{ada}, Body: "x"})
	adaN, creatorN := h.inboxOf(ada)[0], h.inboxOf(creator)[0]
	if err := h.inbox.MarkRead(ctx, ada, []string{creatorN.ID, "not-a-uuid", uid(999999)}); err != nil {
		t.Fatal(err)
	}
	if h.inboxOf(creator)[0].Read() {
		t.Fatal("ada marked the creator's notification read")
	}
	for range 2 {
		if err := h.inbox.MarkRead(ctx, ada, []string{adaN.ID}); err != nil {
			t.Fatal(err)
		}
		if h.unread(ada) != 0 || !h.inboxOf(ada)[0].Read() {
			t.Fatal("not marked read")
		}
		h.check()
	}
	if err := h.inbox.MarkRead(ctx, ada, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty ids: %v", err)
	}
	if err := h.inbox.MarkRead(ctx, ada, make([]string, app.MaxMarkRead+1)); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("too many ids: %v", err)
	}
}

func TestMarkReadAcceptsUppercaseIDs(t *testing.T) {
	h := newHarness(t)
	h.comment(grace, time.Minute, app.CommentEvent{CommentID: "c1", NoteID: note, Body: "x"})
	n := h.inboxOf(creator)[0]
	upper := []byte(n.ID)
	for i, c := range upper {
		if c >= 'a' && c <= 'f' {
			upper[i] = c - 32
		}
	}
	if err := h.inbox.MarkRead(context.Background(), creator, []string{string(upper)}); err != nil {
		t.Fatal(err)
	}
	if h.unread(creator) != 0 {
		t.Fatal("uppercase ID ignored")
	}
}

func TestPreferencesRoundTripAndRejectDuplicates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.inbox.UpdatePreferences(ctx, ada, []domain.Preference{{Kind: domain.KindShare}, {Kind: domain.KindShare}}); !errors.Is(err, domain.ErrDuplicateKind) {
		t.Fatalf("duplicate kind: %v", err)
	}
	if err := h.inbox.UpdatePreferences(ctx, ada, []domain.Preference{{Kind: domain.KindComment, InApp: true, Email: true}}); err != nil {
		t.Fatal(err)
	}
	p, err := h.inbox.Preferences(ctx, ada)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.DefaultPreferences().With([]domain.Preference{{Kind: domain.KindComment, InApp: true, Email: true}})
	if p != want {
		t.Fatalf("preferences %v, want %v", p.List(), want.List())
	}
	if other, _ := h.inbox.Preferences(ctx, grace); other != domain.DefaultPreferences() {
		t.Fatal("another user's preferences changed")
	}
}
