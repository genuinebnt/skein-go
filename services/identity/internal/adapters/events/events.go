// Package events converts between identity's domain values and the
// skein.v1.Event envelope carried on Pub/Sub (proto/skein/v1/events.proto).
//
// Outgoing: every domain.AccessChange becomes an AccessChanged event on
// identity.events, ordered by workspace (the ordering key), because
// consumers apply access changes per workspace and a grant followed by a
// revocation must not arrive reversed.
//
// Incoming: documents.events carries NoteCreated, NoteTrashed, NoteRestored
// and NotePurged, which identity projects; every other payload is
// irrelevant here and is acknowledged without effect.
package events

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/genuinebnt/skein-go/platform/events"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// Topic and subscription names (deploy/compose/pubsub-init.sh).
const (
	Topic                 = "identity.events"
	Source                = "identity"
	DocumentsSubscription = "identity.documents-events"
)

// ErrIrrelevant means an incoming event carries a payload identity does not
// project; the consumer acknowledges it.
var ErrIrrelevant = errors.New("events: payload not used by identity")

// ErrMalformed means an incoming event cannot be decoded; it is
// dead-lettered rather than retried, since retrying cannot fix it.
var ErrMalformed = errors.New("events: malformed event")

// AccessChanged builds the envelope for an access change.
func AccessChanged(ctx context.Context, c domain.AccessChange) (events.Envelope, error) {
	env, err := events.NewEnvelope(ctx, Source, string(c.WorkspaceID), nil)
	if err != nil {
		return events.Envelope{}, err
	}
	msg := &skeinv1.Event{
		Id:          env.ID,
		OccurredAt:  timestamppb.New(c.At),
		Source:      Source,
		OrderingKey: string(c.WorkspaceID),
		Traceparent: env.Traceparent,
		Actor:       &skeinv1.Subject{UserId: string(c.Actor.UserID), AgentRunId: string(c.Actor.AgentRunID)},
		Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{
			WorkspaceId: string(c.WorkspaceID), NoteId: string(c.NoteID), UserId: string(c.UserID), Revoked: c.Revoked,
		}},
	}
	if env.Payload, err = proto.Marshal(msg); err != nil {
		return events.Envelope{}, fmt.Errorf("events: marshal AccessChanged: %w", err)
	}
	return env, nil
}

// DecodeNoteEvent reads a documents event. It returns ErrIrrelevant for
// payloads identity ignores and ErrMalformed for undecodable ones.
func DecodeNoteEvent(msg events.Message) (app.NoteEvent, string, error) {
	var e skeinv1.Event
	if err := proto.Unmarshal(msg.Payload, &e); err != nil {
		return app.NoteEvent{}, "", fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	at := e.GetOccurredAt().AsTime()
	if e.GetOccurredAt() == nil {
		at = msg.PublishTime
	}
	var ev app.NoteEvent
	switch p := e.GetPayload().(type) {
	case *skeinv1.Event_NoteCreated:
		ev = app.NoteEvent{Kind: app.NoteCreated, NoteID: domain.NoteID(p.NoteCreated.GetNoteId()),
			WorkspaceID: domain.WorkspaceID(p.NoteCreated.GetWorkspaceId()), Actor: domain.UserID(e.GetActor().GetUserId())}
		if _, err := domain.ParseUUID("workspace_id", string(ev.WorkspaceID)); err != nil {
			return app.NoteEvent{}, "", fmt.Errorf("%w: NoteCreated workspace: %v", ErrMalformed, err)
		}
	case *skeinv1.Event_NoteTrashed:
		ev = app.NoteEvent{Kind: app.NoteTrashed, NoteID: domain.NoteID(p.NoteTrashed.GetNoteId())}
	case *skeinv1.Event_NoteRestored:
		ev = app.NoteEvent{Kind: app.NoteRestored, NoteID: domain.NoteID(p.NoteRestored.GetNoteId())}
	case *skeinv1.Event_NotePurged:
		ev = app.NoteEvent{Kind: app.NotePurged, NoteID: domain.NoteID(p.NotePurged.GetNoteId())}
	default:
		return app.NoteEvent{}, kindName(&e), ErrIrrelevant
	}
	if _, err := domain.ParseUUID("note_id", string(ev.NoteID)); err != nil {
		return app.NoteEvent{}, kindName(&e), fmt.Errorf("%w: note id: %v", ErrMalformed, err)
	}
	ev.OccurredAt = at.UTC()
	return ev, kindName(&e), nil
}

// kindName is the payload's field name, the skein.event.type metric label.
func kindName(e *skeinv1.Event) string {
	switch e.GetPayload().(type) {
	case *skeinv1.Event_NoteCreated:
		return "note_created"
	case *skeinv1.Event_NoteRenamed:
		return "note_renamed"
	case *skeinv1.Event_NoteTrashed:
		return "note_trashed"
	case *skeinv1.Event_NoteRestored:
		return "note_restored"
	case *skeinv1.Event_NotePurged:
		return "note_purged"
	case *skeinv1.Event_BlocksChanged:
		return "blocks_changed"
	case *skeinv1.Event_LinksChanged:
		return "links_changed"
	case *skeinv1.Event_AccessChanged:
		return "access_changed"
	case *skeinv1.Event_NotificationRequested:
		return "notification_requested"
	default:
		return "unknown"
	}
}
