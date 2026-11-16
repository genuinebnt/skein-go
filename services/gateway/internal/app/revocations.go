package app

import (
	"context"
	"log/slog"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// Revocations closes sockets on every replica when a user loses access.
//
// identity.events reaches the gateway through one subscription
// (gateway.identity-events), so each event is delivered to one replica
// only; that replica applies it locally and broadcasts it on the bus,
// which every replica (itself included, harmlessly) applies too. Closing
// is idempotent, so duplicate deliveries from Pub/Sub and the echo from
// the bus need no deduplication table: the gateway owns no data.
type Revocations struct {
	bus      RevocationBus
	registry *Registry
	log      *slog.Logger
}

// NewRevocations returns the use case.
func NewRevocations(bus RevocationBus, registry *Registry, log *slog.Logger) *Revocations {
	return &Revocations{bus: bus, registry: registry, log: log}
}

// Apply closes this replica's affected sockets and broadcasts rev. A
// broadcast failure is returned so the event is redelivered.
func (r *Revocations) Apply(ctx context.Context, rev domain.Revocation) error {
	closed := r.registry.Revoke(rev)
	r.log.InfoContext(ctx, "access revoked; closing sockets",
		slog.String("user_id", rev.UserID), slog.String("note_id", rev.NoteID),
		slog.String("workspace_id", rev.WorkspaceID), slog.Int("closed", closed))
	return r.bus.Publish(ctx, rev)
}

// Listen applies broadcasts from other replicas until ctx ends.
func (r *Revocations) Listen(ctx context.Context) error {
	return r.bus.Subscribe(ctx, func(rev domain.Revocation) {
		if n := r.registry.Revoke(rev); n > 0 {
			r.log.InfoContext(ctx, "closed sockets for a broadcast revocation",
				slog.String("user_id", rev.UserID), slog.String("note_id", rev.NoteID), slog.Int("closed", n))
		}
	})
}
