package seed

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/documents/internal/app"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/fakes"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

const smallDir = "../../../../testdata/seed/small"

type loaded struct {
	d      *Dataset
	svc    *app.Service
	store  *fakes.Store
	clock  *Clock
	policy *Policy
}

func loadSmall(t *testing.T) loaded {
	t.Helper()
	d, err := Open(smallDir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &Clock{}
	clock.Set(d.ReferenceTime)
	policy, err := NewPolicy(d, clock)
	if err != nil {
		t.Fatal(err)
	}
	replicas, err := NewReplicas(d)
	if err != nil {
		t.Fatal(err)
	}
	store := fakes.NewStore()
	svc := app.New(app.Deps{Store: store, Authorizer: policy, Replicas: replicas, NoteCache: fakes.NewNoteCache(),
		Presence: fakes.NewPresence(clock), Clock: clock}, app.Config{Tick: time.Hour})
	return loaded{d, svc, store, clock, policy}
}

// Every note of the small profile, loaded batch by batch through ApplyOps,
// matches the seed's CRDT truth (text and version vector) and blocks.jsonl
// (block IDs, attributes, texts); every rejected batch is refused for its
// reason; and loading twice changes nothing.
func TestSmallSeedLoadsToItsCRDTTruth(t *testing.T) {
	if testing.Short() {
		t.Skip("replays 432,002 operations")
	}
	ctx := context.Background()
	l := loadSmall(t)
	rep, err := Load(ctx, l.d, l.svc, l.clock, discard())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Notes != 300 || rep.Batches != 1276 || rep.Ops != 432002 || rep.Refused != 13 || rep.Trashed != 2 {
		t.Fatalf("report %+v", rep)
	}
	if err := Verify(ctx, l.d, l.svc); err != nil {
		t.Fatal(err)
	}
	events := len(l.store.Outbox())
	rep, err = Load(ctx, l.d, l.svc, l.clock, discard())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Notes != 0 || rep.AlreadyPresent != 300 || len(l.store.Outbox()) != events {
		t.Fatalf("second load changed something: %+v, %d new events", rep, len(l.store.Outbox())-events)
	}
	if err := Verify(ctx, l.d, l.svc); err != nil {
		t.Fatal(err)
	}
	if err := l.svc.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	created, links := 0, 0
	for _, ev := range l.store.Outbox() {
		switch ev.Kind {
		case domain.EventNoteCreated:
			created++
		case domain.EventLinksChanged:
			links++
		}
	}
	if created != 300 || links == 0 {
		t.Fatalf("%d NoteCreated, %d LinksChanged", created, links)
	}
}

// The loader's policy agrees with the seed's permission truth for every
// user and agent subject on the actions documents checks.
func TestPolicyMatchesThePermissionTruth(t *testing.T) {
	l := loadSmall(t)
	data, err := l.d.readFile("truth/permissions/matrix.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		Subject struct {
			UserID      *string `json:"user_id"`
			AgentRunID  *string `json:"agent_run_id"`
			ShareLinkID *string `json:"share_link_id"`
		} `json:"subject"`
		Action     string `json:"action"`
		ResourceID string `json:"resource_id"`
		At         string `json:"at"`
		Expected   string `json:"expected"`
		Rule       string `json:"rule"`
	}
	rows, err := decodeJSONL[row](data, "matrix")
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]ports.Action{"ACTION_NOTE_READ": ports.ActionRead, "ACTION_NOTE_EDIT": ports.ActionEdit,
		"ACTION_NOTE_DELETE": ports.ActionDelete, "ACTION_NOTE_CREATE": ports.ActionCreate}
	checked := 0
	for _, r := range rows {
		action, ok := actions[r.Action]
		if !ok || r.Subject.ShareLinkID != nil || r.Subject.UserID == nil {
			continue
		}
		at, _ := parseTime(r.At)
		l.clock.Set(at)
		sub := subjectOf(*r.Subject.UserID, r.Subject.AgentRunID)
		got, _ := l.policy.Authorize(context.Background(), sub, []ports.Check{{Action: action, Resource: r.ResourceID}})
		if got[0] != (r.Expected == "allow") {
			raw, _ := json.Marshal(r)
			t.Errorf("policy says %v for %s", got[0], raw)
		}
		checked++
	}
	if checked < 1000 {
		t.Fatalf("only %d rows checked", checked)
	}
	t.Logf("%d permission rows agree", checked)
}

func TestOpenRefusesATamperedDataset(t *testing.T) {
	d, err := Open(smallDir)
	if err != nil {
		t.Fatal(err)
	}
	d.sha["notes.jsonl"] = "00"
	if _, err := readJSONL[noteRecord](d, "notes.jsonl"); err == nil {
		t.Fatal("a hash mismatch must be refused")
	}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
