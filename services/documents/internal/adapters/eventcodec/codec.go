// Package eventcodec turns the documents service's domain events into the
// skein.v1.Event envelopes published on documents.events, and reads the
// identity events documents consumes. The payload is the protobuf Event
// (proto/skein/v1/events.proto); its id, source, ordering key and trace
// context repeat the transport envelope's, so a consumer can work from
// either.
package eventcodec

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// Topic is where documents publishes, ordered by note ID (ADR-0006).
const Topic = "documents.events"

// Source names this service in every envelope.
const Source = "documents"

// AttrEventType carries the payload kind, so subscribers can filter
// without decoding.
const AttrEventType = "event-type"

// Encode builds the envelope for a domain event.
func Encode(ctx context.Context, e domain.Event) (events.Envelope, error) {
	env, err := events.NewEnvelope(ctx, Source, string(e.NoteID), nil)
	if err != nil {
		return env, err
	}
	pb := &skeinv1.Event{
		Id: env.ID, OccurredAt: timestamppb.New(e.At), Source: Source, OrderingKey: env.OrderingKey,
		Traceparent: env.Traceparent,
		Actor:       &skeinv1.Subject{UserId: e.Actor.UserID, AgentRunId: e.Actor.AgentRunID},
	}
	note := string(e.NoteID)
	switch e.Kind {
	case domain.EventNoteCreated:
		pb.Payload = &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: note, WorkspaceId: string(e.WorkspaceID), Title: e.Title}}
	case domain.EventNoteRenamed:
		pb.Payload = &skeinv1.Event_NoteRenamed{NoteRenamed: &skeinv1.NoteRenamed{NoteId: note, Title: e.Title}}
	case domain.EventNoteTrashed:
		pb.Payload = &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: note}}
	case domain.EventNoteRestored:
		pb.Payload = &skeinv1.Event_NoteRestored{NoteRestored: &skeinv1.NoteRestored{NoteId: note}}
	case domain.EventNotePurged:
		pb.Payload = &skeinv1.Event_NotePurged{NotePurged: &skeinv1.NotePurged{NoteId: note}}
	case domain.EventBlocksChanged:
		bc := &skeinv1.BlocksChanged{NoteId: note, RemovedBlockIds: e.RemovedBlockIDs}
		for _, b := range e.Blocks {
			bc.Blocks = append(bc.Blocks, &skeinv1.ChangedBlock{BlockId: b.BlockID, Kind: b.Kind, Language: b.Language, Text: b.Text})
		}
		pb.Payload = &skeinv1.Event_BlocksChanged{BlocksChanged: bc}
	case domain.EventLinksChanged:
		pb.Payload = &skeinv1.Event_LinksChanged{LinksChanged: &skeinv1.LinksChanged{NoteId: note, Targets: e.Links}}
	default:
		return env, fmt.Errorf("eventcodec: unknown event kind %d", e.Kind)
	}
	if env.Payload, err = proto.Marshal(pb); err != nil {
		return env, fmt.Errorf("eventcodec: %w", err)
	}
	env.Attributes = map[string]string{AttrEventType: e.Kind.String()}
	return env, nil
}

// AccessChanged is identity's AccessChanged event, as documents uses it.
type AccessChanged struct {
	Workspace domain.WorkspaceID
	Note      domain.NoteID
	UserID    string
	Revoked   bool
}

// DecodeAccessChanged reads an identity.events message. ok is false for any
// other payload, which documents ignores.
func DecodeAccessChanged(payload []byte) (ev AccessChanged, ok bool, err error) {
	var pb skeinv1.Event
	if err := proto.Unmarshal(payload, &pb); err != nil {
		return ev, false, fmt.Errorf("eventcodec: %w", err)
	}
	ac := pb.GetAccessChanged()
	if ac == nil {
		return ev, false, nil
	}
	return AccessChanged{Workspace: domain.WorkspaceID(ac.GetWorkspaceId()), Note: domain.NoteID(ac.GetNoteId()), UserID: ac.GetUserId(), Revoked: ac.GetRevoked()}, true, nil
}
