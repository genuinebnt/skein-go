package identityevents

import (
	"context"
	"log/slog"
	"testing"

	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

type recorder struct{ got []domain.Revocation }

func (r *recorder) Apply(_ context.Context, rev domain.Revocation) error {
	r.got = append(r.got, rev)
	return nil
}

func message(t *testing.T, e *skeinv1.Event) events.Message {
	t.Helper()
	data, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return events.Message{Envelope: events.Envelope{ID: e.GetId(), Source: "identity", Payload: data}}
}

func TestOnlyRevocationsAreApplied(t *testing.T) {
	r := &recorder{}
	h := Handler(r, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	for _, e := range []*skeinv1.Event{
		{Id: "1", Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{UserId: "u", NoteId: "n", Revoked: true}}},
		{Id: "2", Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{UserId: "u", WorkspaceId: "w", Revoked: true}}},
		{Id: "3", Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{UserId: "u", NoteId: "n"}}},
		{Id: "4", Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: "n"}}},
	} {
		if err := h(ctx, message(t, e)); err != nil {
			t.Fatalf("event %s: %v", e.GetId(), err)
		}
	}
	want := []domain.Revocation{{UserID: "u", NoteID: "n"}, {UserID: "u", WorkspaceID: "w"}}
	if len(r.got) != 2 || r.got[0] != want[0] || r.got[1] != want[1] {
		t.Errorf("applied %v", r.got)
	}
}

func TestUndecodablePayloadsAreNackedTowardTheDeadLetterTopic(t *testing.T) {
	h := Handler(&recorder{}, slog.New(slog.DiscardHandler))
	err := h(context.Background(), events.Message{Envelope: events.Envelope{ID: "bad", Source: "identity", Payload: []byte{0xff, 0xff}}})
	if err == nil {
		t.Error("undecodable payload acked")
	}
}

// Through the in-memory broker: a failed broadcast is redelivered.
func TestBroadcastFailureIsRedelivered(t *testing.T) {
	bus := events.NewMemory(events.MemoryOptions{})
	bus.CreateTopic("identity.events")
	if err := bus.CreateSubscription(Subscription, "identity.events", 5); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	h := Handler(applierFunc(func(context.Context, domain.Revocation) error {
		attempts++
		if attempts == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}), slog.New(slog.DiscardHandler))
	msg := message(t, &skeinv1.Event{Id: "e", Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{UserId: "u", Revoked: true}}})
	if err := bus.Publish(context.Background(), "identity.events", msg.Envelope); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := bus.Deliver(context.Background(), Subscription, h); err != nil {
			t.Fatal(err)
		}
	}
	if attempts != 2 || bus.Pending(Subscription) != 0 {
		t.Errorf("attempts %d, pending %d", attempts, bus.Pending(Subscription))
	}
	if err := bus.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

type applierFunc func(context.Context, domain.Revocation) error

func (f applierFunc) Apply(ctx context.Context, r domain.Revocation) error { return f(ctx, r) }
