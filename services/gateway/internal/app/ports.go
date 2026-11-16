package app

import (
	"context"
	"errors"
	"time"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// ErrNotOwner means a pending idempotency record is no longer held by the
// request that tries to complete or release it (it expired and someone
// else reserved the key).
var ErrNotOwner = errors.New("app: idempotency record has another owner")

// IdempotencyStore keeps idempotency records under scoped keys. All
// methods are atomic per key.
type IdempotencyStore interface {
	// Reserve stores pending under key for ttl unless a record exists. It
	// returns reserved=true when it stored pending, and the existing record
	// otherwise.
	Reserve(ctx context.Context, key string, pending domain.IdempotencyRecord, ttl time.Duration) (existing domain.IdempotencyRecord, reserved bool, err error)
	// Complete replaces the pending record held by owner with complete,
	// for ttl. ErrNotOwner if owner no longer holds it.
	Complete(ctx context.Context, key, owner string, complete domain.IdempotencyRecord, ttl time.Duration) error
	// Release deletes the pending record held by owner. ErrNotOwner if
	// owner no longer holds it.
	Release(ctx context.Context, key, owner string) error
}

// RevocationBus broadcasts revocations to every gateway replica, this one
// included.
type RevocationBus interface {
	Publish(ctx context.Context, r domain.Revocation) error
	// Subscribe calls handle for each broadcast until ctx ends.
	Subscribe(ctx context.Context, handle func(domain.Revocation)) error
}

// SyncRoute is where one note's sync stream goes.
type SyncRoute struct {
	// Owner identifies the documents instance ("10.0.3.7:9090").
	Owner  string
	Client skeinv1.DocumentServiceClient
}

// SyncRouter finds the documents instance that owns a note.
type SyncRouter interface {
	Route(noteID string) (SyncRoute, error)
}

// NoteAccess answers whether a subject may read a note, so a sync socket
// for an unreadable note fails at upgrade with 404 rather than after it.
type NoteAccess interface {
	CanRead(ctx context.Context, subject auth.Subject, noteID string) (bool, error)
}
