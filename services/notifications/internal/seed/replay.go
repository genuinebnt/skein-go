package seed

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/services/notifications/internal/adapters/pubsub"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
)

// ConsumerComments is the consumer name comment events are recorded under
// until documents publishes them.
const ConsumerComments = "notifications.seed-comments"

// Seed agent-run texts, as the generator writes them.
const (
	chatRunTitle    = "Agent run finished"
	editRunTitle    = "Suggested edits ready"
	editRunDelay    = 3 * time.Minute
	chatBodyLength  = 80
	seedEventSource = "seed"
)

// NewDirectory returns an app.Directory over the dataset's users: names,
// addresses, handles and the workspaces each user ever joined.
func NewDirectory(d *Dataset) *fakes.Directory {
	workspaces := map[string][]string{}
	for _, m := range d.Memberships {
		if !slices.Contains(workspaces[m.UserID], m.WorkspaceID) {
			workspaces[m.UserID] = append(workspaces[m.UserID], m.WorkspaceID)
		}
	}
	var users []fakes.DirectoryUser
	for _, u := range d.Users {
		users = append(users, fakes.DirectoryUser{Profile: app.Profile{UserID: u.ID, DisplayName: u.DisplayName, Email: u.Email},
			Handle: u.Handle, Workspaces: workspaces[u.ID]})
	}
	return fakes.NewDirectory(users...)
}

// EventID derives a stable UUID-shaped event ID from what the event is
// about, so replaying twice is recognised as redelivery.
func EventID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	var b [16]byte
	copy(b[:], h.Sum(nil))
	b[6] = b[6]&0x0f | 0x80 // version 8: custom, RFC 9562
	b[8] = b[8]&0x3f | 0x80 // RFC 9562 variant
	return domain.FormatUUID(b)
}

// ReplayStats counts what Replay did.
type ReplayStats struct {
	Events     int
	Duplicates int
	ByType     map[string]int
}

// step is one event of the replay.
type step struct {
	at    time.Time
	order int // NoteCreated before anything else at the same instant
	kind  string
	run   func(ctx context.Context) (app.Outcome, error)
}

// ReplayConfig wires Replay. Store receives the notifications; NewID
// names them (deterministic in tests, UUIDv7 in the command).
type ReplayConfig struct {
	Store  app.Store
	NewID  app.IDGenerator
	Logger *slog.Logger
}

// Replay plays the dataset's events through the service's consumers (see
// the package documentation).
func Replay(ctx context.Context, d *Dataset, cfg ReplayConfig) (ReplayStats, error) {
	bus := &fakes.FanoutBus{}
	producer, err := app.NewProducer(app.ProducerConfig{
		Store: cfg.Store, Authorizer: NewPolicy(d), Directory: NewDirectory(d), UnreadCache: fakes.NewUnreadCache(),
		Fanout: bus.Replica(), Clock: func() time.Time { return d.ReferenceTime }, NewID: cfg.NewID, Logger: cfg.Logger,
	})
	if err != nil {
		return ReplayStats{}, err
	}
	consumers, err := pubsub.NewConsumers(producer, pubsub.Options{Logger: cfg.Logger})
	if err != nil {
		return ReplayStats{}, err
	}
	steps := plan(d, producer, consumers)
	slices.SortStableFunc(steps, func(a, b step) int { return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.order, b.order)) })
	stats := ReplayStats{ByType: map[string]int{}}
	for _, s := range steps {
		outcome, err := s.run(ctx)
		if err != nil {
			return stats, fmt.Errorf("seed: replay %s at %s: %w", s.kind, s.at.Format(time.RFC3339), err)
		}
		stats.Events++
		stats.ByType[s.kind]++
		if outcome == app.OutcomeDuplicate {
			stats.Duplicates++
		}
	}
	return stats, nil
}

// plan lists every event the dataset implies.
func plan(d *Dataset, producer *app.Producer, consumers *pubsub.Consumers) []step {
	var steps []step
	publish := func(at time.Time, order int, kind, subscription string, actor *skeinv1.Subject, attrs map[string]string, ev *skeinv1.Event, key ...string) {
		ev.Id = EventID(append([]string{kind}, key...)...)
		ev.OccurredAt = timestamppb.New(at)
		ev.Actor = actor
		ev.Source = seedEventSource
		steps = append(steps, step{at: at, order: order, kind: kind, run: func(ctx context.Context) (app.Outcome, error) {
			return deliver(ctx, consumers, subscription, ev, attrs, at)
		}})
	}
	user := func(id string) *skeinv1.Subject { return &skeinv1.Subject{UserId: id} }

	blocksByNote := map[string][]*skeinv1.ChangedBlock{}
	for _, b := range d.Blocks {
		kind := b.Type
		if kind != domain.BlockKindCode {
			kind = "paragraph"
		}
		blocksByNote[b.NoteID] = append(blocksByNote[b.NoteID], &skeinv1.ChangedBlock{BlockId: b.BlockID, Kind: kind, Text: b.Text})
	}
	titles := map[string]string{}
	for _, n := range d.Notes {
		titles[n.ID] = n.Title
		publish(n.CreatedAt, 0, "note_created", pubsub.SubscriptionDocuments, user(n.CreatedBy), nil,
			&skeinv1.Event{Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: n.ID, WorkspaceId: n.WorkspaceID, Title: n.Title}}}, n.ID)
		publish(n.UpdatedAt, 1, "blocks_changed", pubsub.SubscriptionDocuments, user(n.CreatedBy), nil,
			&skeinv1.Event{Payload: &skeinv1.Event_BlocksChanged{BlocksChanged: &skeinv1.BlocksChanged{NoteId: n.ID, Blocks: blocksByNote[n.ID]}}}, n.ID)
		if n.TrashedAt != nil {
			publish(*n.TrashedAt, 1, "note_trashed", pubsub.SubscriptionDocuments, user(n.CreatedBy), nil,
				&skeinv1.Event{Payload: &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: n.ID}}}, n.ID)
		}
	}

	authors := map[string]string{}
	for _, c := range d.Comments {
		authors[c.ID] = c.AuthorID
	}
	for _, c := range d.Comments {
		in := app.CommentEvent{CommentID: c.ID, NoteID: c.NoteID, Mentions: c.Mentions, Body: c.Body,
			Location: domain.Location{NoteID: c.NoteID, BlockID: c.BlockID, Start: c.Start, End: c.End}}
		if c.ParentID != nil {
			in.ParentAuthorID = authors[*c.ParentID]
		}
		ev := app.Event{Consumer: ConsumerComments, ID: EventID("comment", c.ID), OccurredAt: c.CreatedAt, ActorUserID: c.AuthorID}
		steps = append(steps, step{at: c.CreatedAt, order: 2, kind: "comment", run: func(ctx context.Context) (app.Outcome, error) {
			return producer.Comment(ctx, ev, in)
		}})
	}

	for _, o := range d.Overrides {
		ws := ""
		for _, n := range d.Notes {
			if n.ID == o.NoteID {
				ws = n.WorkspaceID
			}
		}
		publish(o.CreatedAt, 2, "access_changed", pubsub.SubscriptionIdentity, user(o.GrantedBy), nil,
			&skeinv1.Event{Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{WorkspaceId: ws, NoteId: o.NoteID, UserId: o.UserID}}},
			o.NoteID, o.UserID)
	}
	owners := map[string]string{}
	for _, w := range d.Workspaces {
		owners[w.ID] = w.OwnerID
	}
	for _, m := range d.Memberships {
		if m.RemovedAt != nil {
			publish(*m.RemovedAt, 2, "member_removed", pubsub.SubscriptionIdentity, user(owners[m.WorkspaceID]), nil,
				&skeinv1.Event{Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{WorkspaceId: m.WorkspaceID, UserId: m.UserID, Revoked: true}}},
				m.WorkspaceID, m.UserID)
		}
	}

	finished := skeinv1.NotificationKind_NOTIFICATION_KIND_AGENT_RUN_FINISHED
	for _, c := range d.Chats {
		for _, m := range c.Messages {
			if m.RunID == nil || len(m.Events) == 0 || m.Events[len(m.Events)-1].Kind != "finished" {
				continue
			}
			publish(m.At, 2, "agent_run_finished", pubsub.SubscriptionAgent, &skeinv1.Subject{UserId: c.UserID, AgentRunId: *m.RunID}, nil,
				&skeinv1.Event{Payload: &skeinv1.Event_NotificationRequested{NotificationRequested: &skeinv1.NotificationRequested{
					RecipientId: c.UserID, Kind: finished, Title: chatRunTitle, Body: domain.Truncate(m.Text, chatBodyLength)}}}, *m.RunID)
		}
	}
	noteOfRun := map[string]string{}
	for _, b := range d.Batches {
		if b.AgentRunID != nil {
			noteOfRun[*b.AgentRunID] = b.NoteID
		}
	}
	for _, r := range d.Replicas {
		if r.Kind != "agent" || r.AgentRunID == nil {
			continue
		}
		noteID := noteOfRun[*r.AgentRunID]
		publish(r.RegisteredAt.Add(editRunDelay), 2, "agent_run_finished", pubsub.SubscriptionAgent,
			&skeinv1.Subject{UserId: r.UserID, AgentRunId: *r.AgentRunID}, nil,
			&skeinv1.Event{Payload: &skeinv1.Event_NotificationRequested{NotificationRequested: &skeinv1.NotificationRequested{
				RecipientId: r.UserID, Kind: finished, Title: editRunTitle, Body: "The assistant added a summary to " + titles[noteID] + ".",
				Location: &skeinv1.BlockRef{NoteId: noteID}}}}, *r.AgentRunID)
	}
	return steps
}

// deliver sends one event through the subscription's real handler, as
// Pub/Sub would deliver it.
func deliver(ctx context.Context, consumers *pubsub.Consumers, subscription string, ev *skeinv1.Event, attrs map[string]string, at time.Time) (app.Outcome, error) {
	payload, err := proto.Marshal(ev)
	if err != nil {
		return "", err
	}
	msg := events.Message{Envelope: events.Envelope{ID: ev.GetId(), Source: seedEventSource, Payload: payload, Attributes: attrs},
		PublishTime: at, DeliveryAttempt: 1}
	outcome, err := consumers.Process(ctx, subscription, msg)
	return app.Outcome(outcome), err
}

// Key identifies a notification for comparison: docs/seed-data.md compares
// inboxes by set equality on user, kind and source.
type Key struct {
	UserID     string
	Kind       string
	SourceKind string
	SourceID   string
}

// Diff is the result of Compare.
type Diff struct {
	Expected, Got int
	Missing       []Key // expected but not produced
	Extra         []Key // produced but not expected
}

// OK reports whether the sets are equal.
func (d Diff) OK() bool { return len(d.Missing) == 0 && len(d.Extra) == 0 }

func (d Diff) String() string {
	s := fmt.Sprintf("expected %d, got %d, missing %d, extra %d", d.Expected, d.Got, len(d.Missing), len(d.Extra))
	for _, k := range d.Missing[:min(5, len(d.Missing))] {
		s += fmt.Sprintf("\n  missing %+v", k)
	}
	for _, k := range d.Extra[:min(5, len(d.Extra))] {
		s += fmt.Sprintf("\n  extra   %+v", k)
	}
	return s
}

// Compare checks produced notifications against the expected inbox.
func Compare(expected []Notification, produced []domain.Notification) Diff {
	want := map[Key]bool{}
	for _, n := range expected {
		want[Key{n.UserID, n.Kind, n.Source.Kind, n.Source.ID}] = true
	}
	got := map[Key]bool{}
	for _, n := range produced {
		got[Key{n.RecipientID, n.Kind.ProtoName(), string(n.Source.Kind), n.Source.ID}] = true
	}
	d := Diff{Expected: len(want), Got: len(got)}
	for k := range want {
		if !got[k] {
			d.Missing = append(d.Missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			d.Extra = append(d.Extra, k)
		}
	}
	order := func(a, b Key) int {
		return cmp.Or(cmp.Compare(a.UserID, b.UserID), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.SourceKind, b.SourceKind), cmp.Compare(a.SourceID, b.SourceID))
	}
	slices.SortFunc(d.Missing, order)
	slices.SortFunc(d.Extra, order)
	return d
}
