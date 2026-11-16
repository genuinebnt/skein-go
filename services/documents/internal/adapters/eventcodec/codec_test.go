package eventcodec

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// Consumer-side contract test: what documents publishes decodes as the
// skein.v1.Event the search, ml and notifications consumers expect.
func TestEncodedEventsDecodeAsTheContract(t *testing.T) {
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	ev := domain.Event{
		Kind: domain.EventBlocksChanged, NoteID: "n1", WorkspaceID: "w1", At: at,
		Actor:           domain.Subject{UserID: "u1", AgentRunID: "r1"},
		Blocks:          []domain.ChangedBlock{{BlockID: "7:1", Kind: "code", Language: "go", Text: "x := 1"}},
		RemovedBlockIDs: []string{"7:9"},
	}
	env, err := Encode(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if env.OrderingKey != "n1" || env.Source != Source || env.Attributes[AttrEventType] != "blocks_changed" {
		t.Fatalf("envelope %+v", env)
	}
	var pb skeinv1.Event
	if err := proto.Unmarshal(env.Payload, &pb); err != nil {
		t.Fatal(err)
	}
	bc := pb.GetBlocksChanged()
	if pb.GetId() != env.ID || !pb.GetOccurredAt().AsTime().Equal(at) || pb.GetActor().GetAgentRunId() != "r1" ||
		bc.GetNoteId() != "n1" || bc.GetBlocks()[0].GetLanguage() != "go" || bc.GetRemovedBlockIds()[0] != "7:9" {
		t.Fatalf("payload %v", &pb)
	}
	for kind := domain.EventNoteCreated; kind <= domain.EventLinksChanged; kind++ {
		if _, err := Encode(context.Background(), domain.Event{Kind: kind, NoteID: "n"}); err != nil {
			t.Errorf("%v: %v", kind, err)
		}
	}
}

func TestDecodeAccessChangedIgnoresOtherPayloads(t *testing.T) {
	data, _ := proto.Marshal(&skeinv1.Event{Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{WorkspaceId: "w", UserId: "u", Revoked: true}}})
	ev, ok, err := DecodeAccessChanged(data)
	if err != nil || !ok || ev.UserID != "u" || !ev.Revoked || ev.Note != "" {
		t.Fatalf("%+v %v %v", ev, ok, err)
	}
	other, _ := proto.Marshal(&skeinv1.Event{Payload: &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: "n"}}})
	if _, ok, err := DecodeAccessChanged(other); ok || err != nil {
		t.Fatal("other payloads are ignored")
	}
	if _, _, err := DecodeAccessChanged([]byte{0xff}); err == nil {
		t.Fatal("garbage must fail")
	}
}
