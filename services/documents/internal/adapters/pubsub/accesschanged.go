// Package pubsub consumes the events documents reacts to: identity's
// AccessChanged on the documents.identity-events subscription
// (deploy/compose/pubsub-init.sh). Handling is idempotent without a
// processed-events record, because its effects (dropping cached decisions,
// re-checking open streams) are idempotent and touch no table.
package pubsub

import (
	"context"
	"log/slog"

	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/services/documents/internal/adapters/eventcodec"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// Subscription is documents' subscription to identity.events.
const Subscription = "documents.identity-events"

// AccessHandler is the use case the consumer drives.
type AccessHandler interface {
	AccessChanged(ctx context.Context, ws domain.WorkspaceID, note domain.NoteID, userID string, revoked bool) error
}

// Handler returns the events.Handler for identity.events. Undecodable
// messages are acked and logged: retrying cannot fix them.
func Handler(h AccessHandler, logger *slog.Logger) events.Handler {
	return func(ctx context.Context, msg events.Message) error {
		ev, ok, err := eventcodec.DecodeAccessChanged(msg.Payload)
		if err != nil {
			logger.ErrorContext(ctx, "undecodable identity event", "event_id", msg.ID, "subscription", Subscription, "error", err)
			return nil
		}
		if !ok {
			return nil
		}
		return h.AccessChanged(ctx, ev.Workspace, ev.Note, ev.UserID, ev.Revoked)
	}
}
