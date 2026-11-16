package pubsub_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/genuinebnt/skein-go/platform/events"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	identityevents "github.com/genuinebnt/skein-go/services/identity/internal/adapters/events"
	"github.com/genuinebnt/skein-go/services/identity/internal/adapters/pubsub"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
	"github.com/genuinebnt/skein-go/services/identity/internal/fakes"
)

const (
	ws   = "019c8cf2-1000-769b-b000-73aede463976"
	note = "019cc260-0a80-72dd-9e8d-ba3c76c67531"
	user = "019b7892-1c00-75eb-b4c7-d9d89d6544c6"
)

func publish(t *testing.T, bus *events.Memory, e *skeinv1.Event) {
	t.Helper()
	payload, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := events.NewEnvelope(context.Background(), "documents", note, payload)
	if err := bus.Publish(context.Background(), "documents.events", env); err != nil {
		t.Fatal(err)
	}
}

// TestDocumentsEventsMaintainTheProjectionExactlyOnce sends documents'
// events through the in-memory bus, redelivering one, and checks the
// projection through Authorize.
func TestDocumentsEventsMaintainTheProjectionExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := fakes.NewStore()
	clock := fakes.NewClock(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	hasher, _ := credential.NewHasher(credential.HasherOptions{Params: credential.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}})
	svc, _ := app.New(app.DefaultConfig(), app.Deps{Store: store, Hasher: hasher, Issuer: fakes.NewIssuer(clock.Now), Clock: clock.Now})
	// The workspace and its member exist in identity already.
	_ = store.InTx(ctx, func(ctx context.Context, tx app.Tx) error {
		_ = tx.InsertUser(ctx, domain.User{ID: user, Email: "m@example.com", CreatedAt: clock.Now()})
		_ = tx.InsertWorkspace(ctx, domain.Workspace{ID: ws, OwnerID: user, CreatedAt: clock.Now()})
		return tx.UpsertMembership(ctx, domain.Membership{WorkspaceID: ws, UserID: user, Role: domain.WorkspaceRoleMember, JoinedAt: clock.Now().Add(-time.Hour)})
	})
	bus := events.NewMemory(events.MemoryOptions{})
	bus.CreateTopic("documents.events")
	if err := bus.CreateSubscription(identityevents.DocumentsSubscription, "documents.events", pubsub.MaxDeliveryAttempts); err != nil {
		t.Fatal(err)
	}
	consumer, err := pubsub.NewConsumer(svc, identityevents.DocumentsSubscription, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := timestamppb.New(clock.Now())
	actor := &skeinv1.Subject{UserId: user}
	publish(t, bus, &skeinv1.Event{OccurredAt: at, Actor: actor, Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: note, WorkspaceId: ws}}})
	publish(t, bus, &skeinv1.Event{OccurredAt: at, Payload: &skeinv1.Event_BlocksChanged{BlocksChanged: &skeinv1.BlocksChanged{NoteId: note}}})
	publish(t, bus, &skeinv1.Event{OccurredAt: at, Payload: &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: note}}})
	if _, err := bus.Deliver(ctx, identityevents.DocumentsSubscription, consumer.Handle); err != nil {
		t.Fatal(err)
	}
	read := func() bool {
		d, err := svc.Authorize(ctx, domain.UserSubject(user, ""), []domain.Check{{Action: domain.ActionNoteRead, ResourceID: note}})
		if err != nil {
			t.Fatal(err)
		}
		return d[0].Allowed
	}
	if read() {
		t.Fatal("trashed note readable by a member")
	}
	// A redelivery of the trash event after a restore must not re-trash.
	trashed := bus.Published("documents.events")[2]
	publish(t, bus, &skeinv1.Event{OccurredAt: at, Payload: &skeinv1.Event_NoteRestored{NoteRestored: &skeinv1.NoteRestored{NoteId: note}}})
	_ = bus.Publish(ctx, "documents.events", trashed)
	if _, err := bus.Deliver(ctx, identityevents.DocumentsSubscription, consumer.Handle); err != nil {
		t.Fatal(err)
	}
	if !read() {
		t.Fatal("a redelivered NoteTrashed was applied twice")
	}
	if err := bus.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedEventsAreRetriedThenDeadLettered(t *testing.T) {
	ctx := context.Background()
	bus := events.NewMemory(events.MemoryOptions{})
	bus.CreateTopic("documents.events")
	_ = bus.CreateSubscription("s", "documents.events", 3)
	calls := 0
	consumer, _ := pubsub.NewConsumer(applierFunc(func() { calls++ }), "s", nil, nil)
	env, _ := events.NewEnvelope(ctx, "documents", "k", []byte{0xff, 0xff})
	_ = bus.Publish(ctx, "documents.events", env)
	bad, _ := proto.Marshal(&skeinv1.Event{Payload: &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: "not-a-uuid"}}})
	env2, _ := events.NewEnvelope(ctx, "documents", "k2", bad)
	_ = bus.Publish(ctx, "documents.events", env2)
	for range 5 {
		_, _ = bus.Deliver(ctx, "s", consumer.Handle)
	}
	if n := len(bus.DeadLetters("s")); n != 2 || calls != 0 {
		t.Fatalf("%d dead letters, %d applications; want 2 and 0", n, calls)
	}
}

type applierFunc func()

func (f applierFunc) ApplyNoteEvent(context.Context, string, string, app.NoteEvent) (app.ConsumeOutcome, error) {
	f()
	return app.OutcomeApplied, nil
}
