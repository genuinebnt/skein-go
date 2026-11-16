package events

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/genuinebnt/skein-go/platform/events"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

func TestAccessChangedCarriesTheChangeOrderedByWorkspace(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	c := domain.AccessChange{WorkspaceID: "ws", NoteID: "n", UserID: "u", Revoked: true,
		Actor: domain.UserSubject("actor", "run"), At: at}
	env, err := AccessChanged(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if env.Source != "identity" || env.OrderingKey != "ws" || env.Validate() != nil {
		t.Fatalf("envelope %+v", env)
	}
	var e skeinv1.Event
	if err := proto.Unmarshal(env.Payload, &e); err != nil {
		t.Fatal(err)
	}
	ac := e.GetAccessChanged()
	if e.GetId() != env.ID || !e.GetOccurredAt().AsTime().Equal(at) || e.GetActor().GetAgentRunId() != "run" ||
		ac.GetWorkspaceId() != "ws" || ac.GetNoteId() != "n" || ac.GetUserId() != "u" || !ac.GetRevoked() {
		t.Fatalf("event %v", &e)
	}
}

func FuzzDecodeNoteEvent(f *testing.F) {
	for _, e := range []*skeinv1.Event{
		{Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: "019cc260-0a80-72dd-9e8d-ba3c76c67531", WorkspaceId: "019c8cf2-1000-769b-b000-73aede463976"}}},
		{Payload: &skeinv1.Event_NotePurged{NotePurged: &skeinv1.NotePurged{NoteId: "x"}}},
		{Payload: &skeinv1.Event_LinksChanged{LinksChanged: &skeinv1.LinksChanged{NoteId: "x"}}},
	} {
		b, _ := proto.Marshal(e)
		f.Add(b)
	}
	f.Add([]byte{0x0a, 0xff})
	f.Fuzz(func(t *testing.T, payload []byte) {
		ev, _, err := DecodeNoteEvent(events.Message{Envelope: events.Envelope{Payload: payload}})
		if err != nil {
			if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrIrrelevant) {
				t.Fatalf("unexpected error kind %v", err)
			}
			return
		}
		if _, err := domain.ParseUUID("note_id", string(ev.NoteID)); err != nil {
			t.Fatalf("accepted note ID %q", ev.NoteID)
		}
	})
}
