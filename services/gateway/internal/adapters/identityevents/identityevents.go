// Package identityevents consumes identity.events for the gateway
// (subscription gateway.identity-events in deploy/compose/pubsub-init.sh).
//
// Only AccessChanged with revoked set (access went down: removal,
// demotion, a lowered override, a revoked link) matters here: it becomes
// an app.Revocations.Apply, which re-checks the user's sync sockets and
// closes those that can no longer read, on every replica. Grants need
// nothing, because documents re-checks each batch of operations with the
// current role. Other payloads are acked and ignored.
//
// A payload that does not decode is returned as an error, so Pub/Sub
// redelivers it and finally moves it to the dead-letter topic, where a
// person can look at it; acking it would lose the evidence. Delivery is at
// least once and closing sockets is idempotent, so no processed-event
// table is kept (the gateway has no database).
//
// # Porting notes
//
// Rust: a prost decode of skein.v1.Event and a match on the payload
// variant.
package identityevents

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// Subscription is the gateway's subscription to identity.events.
const Subscription = "gateway.identity-events"

// Applier applies a revocation; app.Revocations implements it.
type Applier interface {
	Apply(ctx context.Context, r domain.Revocation) error
}

// Handler returns the events.Handler for identity.events.
func Handler(applier Applier, log *slog.Logger) events.Handler {
	return func(ctx context.Context, msg events.Message) error {
		var event skeinv1.Event
		if err := proto.Unmarshal(msg.Payload, &event); err != nil {
			log.ErrorContext(ctx, "identity event does not decode", slog.String("event_id", msg.ID), slog.Int("delivery_attempt", msg.DeliveryAttempt))
			return fmt.Errorf("identityevents: decode %s: %w", msg.ID, err)
		}
		changed := event.GetAccessChanged()
		if changed == nil || !changed.GetRevoked() || changed.GetUserId() == "" {
			return nil
		}
		return applier.Apply(ctx, domain.Revocation{
			UserID:      changed.GetUserId(),
			WorkspaceID: changed.GetWorkspaceId(),
			NoteID:      changed.GetNoteId(),
		})
	}
}
