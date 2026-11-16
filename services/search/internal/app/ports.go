package app

import (
	"context"
	"errors"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// AccessDirectory answers which notes a subject can read: identity's
// ListReadableNotes. Implementations return ErrNotFound when the subject
// may not see the workspace at all.
type AccessDirectory interface {
	ListReadableNotes(ctx context.Context, subject auth.Subject, workspaceID string) ([]string, error)
}

// NotePair is an unordered pair of notes, From < To.
type NotePair struct {
	From, To string
}

// Similarity gives the cosine similarity of note embeddings: ml's view of
// meaning, used for semantic path weights.
type Similarity interface {
	// NoteCosines returns the cosine of each pair it knows; pairs it does
	// not know are left out.
	NoteCosines(ctx context.Context, workspaceID string, pairs []NotePair) (map[NotePair]float64, error)
}

// Relevance scores notes against a free-text topic: ml's semantic search,
// used by reading plans.
type Relevance interface {
	// TopicRelevance returns a score in [0, 1] for notes related to topic;
	// unrelated notes are left out.
	TopicRelevance(ctx context.Context, workspaceID, topic string) (map[string]float64, error)
}

// Change is one entry of the change feed: note changed at position Seq.
type Change struct {
	Seq    int64
	NoteID string
}

// Snapshot is a stored projection snapshot.
type Snapshot struct {
	Position int64
	Data     []byte
}

// Store is the durable projection: note states, the change feed, the
// record of processed events, and snapshots, all in the search schema.
type Store interface {
	// Apply applies m to its note's stored state with domain.ApplyMutation
	// and appends the note to the change feed, unless consumer already
	// processed eventID, all in one transaction; it reports whether the
	// event was new. A nil m records the event as processed and changes
	// nothing else (events search ignores). It also records eventID as the
	// consumer's last applied event. A purge deletes every stored snapshot
	// in the same transaction, since snapshots hold note text.
	Apply(ctx context.Context, consumer, eventID string, m *domain.Mutation) (bool, error)
	// Changes returns up to limit changes after position, ascending. It
	// returns ErrFeedGap when changes after position have been pruned.
	Changes(ctx context.Context, after int64, limit int) ([]Change, error)
	// LoadNotes returns the stored states of ids; unknown notes are absent.
	LoadNotes(ctx context.Context, ids []string) (map[string]*domain.NoteState, error)
	// LoadAll calls fn with every stored note state and returns the feed
	// position they reflect, read consistently.
	LoadAll(ctx context.Context, fn func(*domain.NoteState) error) (int64, error)
	// LatestSnapshot returns the newest snapshot, if any.
	LatestSnapshot(ctx context.Context) (Snapshot, bool, error)
	// SaveSnapshot stores a snapshot and drops all but the newest few.
	SaveSnapshot(ctx context.Context, s Snapshot) error
}

// ErrFeedGap means the change feed no longer holds the changes a follower
// needs; it must reload every note.
var ErrFeedGap = errors.New("search: change feed pruned past position")
