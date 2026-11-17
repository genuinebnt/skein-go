package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// RecheckTimeout bounds one sync socket's read-access re-check.
const RecheckTimeout = 5 * time.Second

// Revocations enforces lowered access on every replica's sockets.
//
// identity sets AccessChanged.revoked whenever access went down: removal,
// but also a demotion such as member to viewer, which still reads. So a
// revocation is a reason to re-check, not a verdict. Sync sockets re-check
// read access as their own user: still readable stays open (documents
// rejects the viewer's edits batch by batch, so the socket is effectively
// read-only), unreadable closes with 4403, and a failed check closes with
// 1012 so the client reconnects and is authorised afresh (fail closed
// without claiming access was lost). Agent sockets close with 4403 on a
// workspace-level revocation: the gateway does not know a chat's
// workspace, and a reconnect is cheap.
//
// identity.events reaches the gateway through one subscription
// (gateway.identity-events), so each event is delivered to one replica
// only; that replica applies it locally and broadcasts it on the bus,
// which every replica (itself included, harmlessly) applies too. Applying
// is idempotent, so duplicate deliveries from Pub/Sub and the echo from
// the bus need no deduplication table: the gateway owns no data.
type Revocations struct {
	bus      RevocationBus
	registry *Registry
	access   NoteAccess
	log      *slog.Logger
}

// NewRevocations returns the use case. access re-checks sync sockets.
func NewRevocations(bus RevocationBus, registry *Registry, access NoteAccess, log *slog.Logger) *Revocations {
	return &Revocations{bus: bus, registry: registry, access: access, log: log}
}

// RevocationOutcome counts what one revocation did to this replica's sockets.
type RevocationOutcome struct {
	// Closed lost access (4403); Kept can still read; Unverified could not
	// be re-checked and were asked to reconnect (1012).
	Closed, Kept, Unverified int
}

// Apply enforces rev on this replica's sockets and broadcasts it. A
// broadcast failure is returned so the event is redelivered.
func (r *Revocations) Apply(ctx context.Context, rev domain.Revocation) error {
	o := r.Enforce(ctx, rev)
	r.log.InfoContext(ctx, "access lowered; re-checked sockets",
		slog.String("user_id", rev.UserID), slog.String("note_id", rev.NoteID),
		slog.String("workspace_id", rev.WorkspaceID),
		slog.Int("closed", o.Closed), slog.Int("kept", o.Kept), slog.Int("unverified", o.Unverified))
	return r.bus.Publish(ctx, rev)
}

// Listen applies broadcasts from other replicas until ctx ends.
func (r *Revocations) Listen(ctx context.Context) error {
	return r.bus.Subscribe(ctx, func(rev domain.Revocation) {
		if o := r.Enforce(ctx, rev); o != (RevocationOutcome{}) {
			r.log.InfoContext(ctx, "re-checked sockets for a broadcast revocation",
				slog.String("user_id", rev.UserID), slog.String("note_id", rev.NoteID),
				slog.Int("closed", o.Closed), slog.Int("kept", o.Kept), slog.Int("unverified", o.Unverified))
		}
	})
}

// Enforce re-checks or closes every local socket rev affects.
func (r *Revocations) Enforce(ctx context.Context, rev domain.Revocation) RevocationOutcome {
	var o RevocationOutcome
	for _, s := range r.registry.affected(rev) {
		if s.info.Kind != domain.SocketSync {
			s.closer.Close(domain.CloseForbidden, "access revoked")
			o.Closed++
			continue
		}
		readable, err := r.canStillRead(ctx, s)
		switch {
		case err != nil:
			r.log.WarnContext(ctx, "could not re-check a sync socket's access; asking it to reconnect",
				slog.String("user_id", s.info.UserID), slog.String("note_id", s.info.NoteID), slog.Any("error", err))
			s.closer.Close(domain.CloseServiceRestart, "access changed; reconnect")
			o.Unverified++
		case readable:
			o.Kept++
		default:
			s.closer.Close(domain.CloseForbidden, "access revoked")
			o.Closed++
		}
	}
	return o
}

// canStillRead asks NoteAccess as the socket's user, with the event's
// trace and cancellation and a bounded deadline. The claims were verified
// when the socket opened and are still valid: a socket closes when its
// token expires.
func (r *Revocations) canStillRead(ctx context.Context, s entry) (bool, error) {
	subject := auth.Subject{UserID: s.info.UserID}
	if s.credentials != nil {
		if claims, ok := auth.ClaimsFromContext(s.credentials); ok {
			token, _ := auth.TokenFromContext(s.credentials)
			ctx = auth.WithClaims(ctx, claims, token)
			subject = claims.Subject
		}
	}
	ctx, cancel := context.WithTimeout(ctx, RecheckTimeout)
	defer cancel()
	return r.access.CanRead(ctx, subject, s.info.NoteID)
}
