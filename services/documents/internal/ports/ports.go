// Package ports defines the interfaces the documents use cases need from
// the outside world (the store, identity, Redis, the clock) and the small
// value types they exchange. Adapters implement them for production and
// package fakes in memory for tests; package app re-exports them. They sit in
// their own package so fakes and the app's tests can both import them.
//
// Rust: one trait per interface, injected as Arc<dyn Trait>.
package ports

import (
	"context"
	"time"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// Store is the service's own Postgres schema: notes, the append-only op
// log, snapshots, the published projection state and the outbox. Every
// method that takes events writes them to the outbox in the same
// transaction as the change, so an event is published if and only if its
// change committed (ADR-0006).
type Store interface {
	// InsertNote creates a note with its first batch (nil for an empty
	// note) and its initial projection. ErrAlreadyExists if the ID exists.
	InsertNote(ctx context.Context, note domain.Note, first *domain.Batch, projection domain.Projection, events []domain.Event) error
	// GetNote returns a note, trashed or not. ErrNotFound if it does not
	// exist or was purged.
	GetNote(ctx context.Context, id domain.NoteID) (domain.Note, error)
	// ListNotes returns notes of a workspace among IDs, ascending by ID,
	// after After, at most Limit.
	ListNotes(ctx context.Context, q NoteQuery) ([]domain.Note, error)
	// UpdateNote stores a note's metadata (title, trash, updated_at).
	UpdateNote(ctx context.Context, note domain.Note, events []domain.Event) error

	// AppendBatches appends batches to a note's op log and sets its
	// updated_at. The batches' Seq continue the log; ErrConflict if one is
	// taken (another writer), and then nothing is written.
	AppendBatches(ctx context.Context, id domain.NoteID, batches []domain.Batch, updatedAt time.Time) error
	// LoadBatches returns every batch of a note in order; Ops and Attrs are
	// set only for batches after payloadsAfter.
	LoadBatches(ctx context.Context, id domain.NoteID, payloadsAfter int64) ([]domain.Batch, error)
	// LoadBatch returns one batch with its payload.
	LoadBatch(ctx context.Context, id domain.NoteID, seq int64) (domain.Batch, error)
	// SaveSnapshot stores a checkpoint; LatestSnapshot returns the newest.
	SaveSnapshot(ctx context.Context, id domain.NoteID, s domain.Snapshot) error
	LatestSnapshot(ctx context.Context, id domain.NoteID) (domain.Snapshot, bool, error)

	// LoadProjection returns what was last published about a note.
	LoadProjection(ctx context.Context, id domain.NoteID) (domain.Projection, error)
	// PublishProjection stores the new projection state and its events,
	// unless a state at least as new is already stored (a second actor
	// raced this one); it reports whether it wrote.
	PublishProjection(ctx context.Context, id domain.NoteID, p domain.Projection, events []domain.Event) (bool, error)
	// StaleProjections returns notes whose op log is ahead of their
	// published projection (a crash between commit and publication).
	StaleProjections(ctx context.Context, limit int) ([]domain.NoteID, error)

	// TrashedBefore returns notes trashed before t, for purging.
	TrashedBefore(ctx context.Context, t time.Time, limit int) ([]domain.NoteID, error)
	// PurgeNote erases a note's op log, snapshots and projection, keeps a
	// tombstone so the ID stays NOT_FOUND, and writes the events.
	PurgeNote(ctx context.Context, id domain.NoteID, events []domain.Event) error
}

// NoteQuery selects notes for ListNotes.
type NoteQuery struct {
	Workspace      domain.WorkspaceID
	IDs            []domain.NoteID
	After          domain.NoteID
	Limit          int
	IncludeTrashed bool
}

// Action is a permission checked with identity (skein.v1.Action).
type Action uint8

// The actions documents checks.
const (
	ActionRead   Action = 1
	ActionEdit   Action = 3
	ActionCreate Action = 4
	ActionDelete Action = 5
)

// Check is one question for the Authorizer: may the subject do Action to
// Resource (a note ID, or a workspace ID for ActionCreate)?
type Check struct {
	Action   Action
	Resource string
}

// Authorizer is identity's Authorize and ListReadableNotes (ADR-0008),
// behind a short cache that AccessChanged events invalidate.
type Authorizer interface {
	Authorize(ctx context.Context, subject domain.Subject, checks []Check) ([]bool, error)
	ReadableNotes(ctx context.Context, subject domain.Subject, workspace domain.WorkspaceID) ([]domain.NoteID, error)
}

// AuthCache is the cache in front of the Authorizer, when there is one.
type AuthCache interface {
	// InvalidateUser drops every cached decision about a user.
	InvalidateUser(ctx context.Context, userID string) error
}

// ReplicaRegistry checks replica ownership with identity's RegisterReplica,
// which succeeds for a replica the caller owns (registering it if new) and
// fails for one registered to someone else (ADR-0004).
type ReplicaRegistry interface {
	// EnsureReplica returns ErrPermissionDenied if the replica belongs to
	// another user.
	EnsureReplica(ctx context.Context, subject domain.Subject, replica crdt.ReplicaID) error
}

// NoteCache caches note metadata (Redis): GetNote and every access check
// read it, and it changes rarely. Writes delete the entry.
type NoteCache interface {
	Get(ctx context.Context, id domain.NoteID) (domain.Note, bool)
	Set(ctx context.Context, note domain.Note)
	Delete(ctx context.Context, id domain.NoteID)
}

// Presence is a cursor in a note, anchored to element IDs.
type Presence struct {
	UserID  string
	Replica crdt.ReplicaID
	Anchor  string
	Head    string
}

// PresenceStore keeps cursors with a TTL (Redis), so a client that vanishes
// without closing disappears, and a new session sees who is there.
type PresenceStore interface {
	Put(ctx context.Context, id domain.NoteID, p Presence, ttl time.Duration) error
	List(ctx context.Context, id domain.NoteID) ([]Presence, error)
	Remove(ctx context.Context, id domain.NoteID, replica crdt.ReplicaID) error
}

// Clock tells the time. The seed loader fixes it to the dataset's times.
type Clock interface {
	Now() time.Time
}

// SystemClock is the real clock.
type SystemClock struct{}

// Now returns the current time in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }
