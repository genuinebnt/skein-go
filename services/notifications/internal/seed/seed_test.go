package seed

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
)

const smallDir = "../../../../testdata/seed/small"

func openSmall(t *testing.T) *Dataset {
	t.Helper()
	d, err := Open(smallDir)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestPolicyMatchesThePermissionTruth checks the replay's stand-in for
// identity against truth/permissions/matrix.jsonl, for every user subject
// and every action this service asks about.
func TestPolicyMatchesThePermissionTruth(t *testing.T) {
	d := openSmall(t)
	p := NewPolicy(d)
	data, err := os.ReadFile(filepath.Join(smallDir, "truth/permissions/matrix.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		Subject struct {
			UserID      *string `json:"user_id"`
			AgentRunID  *string `json:"agent_run_id"`
			ShareLinkID *string `json:"share_link_id"`
		} `json:"subject"`
		Action     string    `json:"action"`
		ResourceID string    `json:"resource_id"`
		At         time.Time `json:"at"`
		Expected   string    `json:"expected"`
		Rule       string    `json:"rule"`
	}
	var rows []row
	if err := lines(data, &rows); err != nil {
		t.Fatal(err)
	}
	actions := map[string]app.Action{"ACTION_NOTE_READ": app.ActionNoteRead, "ACTION_NOTE_COMMENT": app.ActionNoteComment,
		"ACTION_NOTE_EDIT": app.ActionNoteEdit, "ACTION_NOTE_CREATE": app.ActionNoteCreate}
	checked := 0
	for _, r := range rows {
		action, ok := actions[r.Action]
		if !ok || r.Subject.UserID == nil || r.Subject.AgentRunID != nil || r.Subject.ShareLinkID != nil {
			continue
		}
		got := p.Allowed(*r.Subject.UserID, action, r.ResourceID, r.At)
		if got != (r.Expected == "allow") {
			t.Errorf("%s %s on %s: allowed=%t, truth says %s (%s)", *r.Subject.UserID, r.Action, r.ResourceID, got, r.Expected, r.Rule)
		}
		checked++
	}
	if checked < 1000 {
		t.Fatalf("only %d rows checked", checked)
	}
	t.Logf("%d permission rows agree", checked)
}

// TestProducerRulesReproduceTheSeedInbox is the rule check of the loader
// contract: replaying the dataset's events through the real consumers
// yields exactly the expected inbox (set equality on user, kind, source).
func TestProducerRulesReproduceTheSeedInbox(t *testing.T) {
	d := openSmall(t)
	store := fakes.NewStore()
	stats, err := Replay(context.Background(), d, ReplayConfig{Store: store, NewID: fakes.IDs(7)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	produced := store.Notifications()
	diff := Compare(d.Notifications, produced)
	if !diff.OK() {
		t.Fatalf("producer rules disagree with notifications.jsonl: %s", diff)
	}
	if stats.ByType["blocks_changed"] != len(d.Notes) || stats.ByType["comment"] != len(d.Comments) || stats.Duplicates != 0 {
		t.Errorf("stats %+v", stats)
	}
	for _, n := range produced {
		if n.Source.Kind == domain.SourceBlockMention {
			t.Errorf("block text in the seed mentioned someone: %+v", n)
		}
	}
	t.Logf("%d notifications from %d events match the seed", len(produced), stats.Events)
}

func TestReplayingTwiceIsAllDuplicates(t *testing.T) {
	d := openSmall(t)
	store := fakes.NewStore()
	ctx := context.Background()
	if _, err := Replay(ctx, d, ReplayConfig{Store: store, NewID: fakes.IDs(7)}); err != nil {
		t.Fatal(err)
	}
	before := store.Notifications()
	stats, err := Replay(ctx, d, ReplayConfig{Store: store, NewID: fakes.IDs(8)})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Duplicates != stats.Events {
		t.Fatalf("second replay applied %d of %d events", stats.Events-stats.Duplicates, stats.Events)
	}
	if after := store.Notifications(); len(after) != len(before) {
		t.Fatalf("second replay changed the store: %d -> %d", len(before), len(after))
	}
}

func TestLoadingTheInboxKeepsIDsAndIsIdempotent(t *testing.T) {
	d := openSmall(t)
	store := fakes.NewStore()
	ctx := context.Background()
	first, err := LoadInbox(ctx, store, d)
	if err != nil {
		t.Fatal(err)
	}
	if first.Loaded != len(d.Notifications) || first.AlreadyPresent != 0 {
		t.Fatalf("first load %+v", first)
	}
	if err := store.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	second, err := LoadInbox(ctx, store, d)
	if err != nil {
		t.Fatal(err)
	}
	if second.Loaded != 0 || second.AlreadyPresent != len(d.Notifications) {
		t.Fatalf("second load %+v", second)
	}
	byID := map[string]domain.Notification{}
	for _, n := range store.Notifications() {
		byID[n.ID] = n
	}
	for _, want := range d.Notifications {
		got, ok := byID[want.ID]
		if !ok || got.Read() != want.Read || got.RecipientID != want.UserID {
			t.Fatalf("notification %s: stored %+v", want.ID, got)
		}
	}
	if len(store.Jobs()) != 0 {
		t.Fatal("loading the seed enqueued email")
	}
	// The loaded inbox matches the rules too, and a replay on top of it is
	// recognised notification by notification (no duplicates by source).
	if _, err := Replay(ctx, d, ReplayConfig{Store: store, NewID: fakes.IDs(9)}); err != nil {
		t.Fatal(err)
	}
	if n := len(store.Notifications()); n != len(d.Notifications) {
		t.Fatalf("replay on the loaded inbox left %d notifications, want %d", n, len(d.Notifications))
	}
}

func TestUnreadCountsOfTheLoadedInboxMatchTheSeed(t *testing.T) {
	d := openSmall(t)
	store := fakes.NewStore()
	if _, err := LoadInbox(context.Background(), store, d); err != nil {
		t.Fatal(err)
	}
	want := map[string]uint32{}
	for _, n := range d.Notifications {
		if !n.Read {
			want[n.UserID]++
		}
	}
	inbox := app.NewInbox(store, fakes.NewUnreadCache(), nil, nil)
	for _, u := range d.Users {
		got, err := inbox.UnreadCount(context.Background(), u.ID)
		if err != nil || got != want[u.ID] {
			t.Errorf("user %s: unread %d (%v), want %d", u.ID, got, err, want[u.ID])
		}
	}
}

func TestADatasetThatDoesNotMatchItsManifestIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, name := range append([]string{"manifest.json"}, Files...) {
		data, err := os.ReadFile(filepath.Join(smallDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "comments.jsonl" {
			data = append(data, '\n')
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(dir); !errors.Is(err, ErrRefused) {
		t.Fatalf("tampered dataset: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(smallDir, "manifest.json"))
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["format_version"] = 2
	raw, _ = json.Marshal(m)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600)
	if _, err := Open(dir); !errors.Is(err, ErrRefused) {
		t.Fatalf("unknown format version: %v", err)
	}
}

func TestEventIDsAreStableUUIDs(t *testing.T) {
	a, b := EventID("comment", "x"), EventID("comment", "x")
	if a != b || !domain.ValidUUID(a) || a == EventID("comment", "y") || EventID("a", "bc") == EventID("ab", "c") {
		t.Fatalf("EventID: %s %s", a, b)
	}
}
