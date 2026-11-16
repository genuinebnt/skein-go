package pubsub

import (
	"context"
	"log/slog"
	"testing"

	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

type recorder struct{ calls []string }

func (r *recorder) AccessChanged(_ context.Context, ws domain.WorkspaceID, note domain.NoteID, user string, revoked bool) error {
	r.calls = append(r.calls, string(ws)+"/"+string(note)+"/"+user)
	return nil
}

func TestAccessChangedReachesTheUseCaseAndOtherMessagesAreAcked(t *testing.T) {
	bus := events.NewMemory(events.MemoryOptions{})
	bus.CreateTopic("identity.events")
	if err := bus.CreateSubscription(Subscription, "identity.events", 5); err != nil {
		t.Fatal(err)
	}
	payload, _ := proto.Marshal(&skeinv1.Event{Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{WorkspaceId: "w", NoteId: "n", UserId: "u", Revoked: true}}})
	other, _ := proto.Marshal(&skeinv1.Event{Payload: &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: "n"}}})
	for _, p := range [][]byte{payload, other, {0xff}} {
		env, _ := events.NewEnvelope(context.Background(), "identity", "u", p)
		if err := bus.Publish(context.Background(), "identity.events", env); err != nil {
			t.Fatal(err)
		}
	}
	r := &recorder{}
	if _, err := bus.Deliver(context.Background(), Subscription, Handler(r, slog.New(slog.DiscardHandler))); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != "w/n/u" || bus.Pending(Subscription) != 0 {
		t.Fatalf("calls %v, pending %d", r.calls, bus.Pending(Subscription))
	}
}
