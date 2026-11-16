// Package fakes holds in-memory implementations of the search service's
// ports, for unit tests and the seed loader's dry runs. Each mirrors the
// contract documented on its port, including the failure modes tests
// need to inject.
package fakes

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// Store is an in-memory app.Store with the same semantics as the Postgres
// adapter: idempotent apply per (consumer, event), a change feed with
// positions in commit order, snapshot retention, and purges that delete
// snapshots.
type Store struct {
	mu            sync.Mutex
	notes         map[string]*domain.NoteState
	processed     map[string]bool
	positions     map[string]string
	changes       []app.Change
	seq           int64
	prunedThrough int64
	snapshots     []app.Snapshot

	// FailNextApply, when set, makes the next Apply fail with it before
	// changing anything (a lost transaction).
	FailNextApply error
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{notes: map[string]*domain.NoteState{}, processed: map[string]bool{}, positions: map[string]string{}}
}

// Apply implements app.Store.
func (s *Store) Apply(_ context.Context, consumer, eventID string, m *domain.Mutation) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.FailNextApply; err != nil {
		s.FailNextApply = nil
		return false, err
	}
	key := consumer + "|" + eventID
	if s.processed[key] {
		return false, nil
	}
	if m != nil {
		next, err := domain.ApplyMutation(s.notes[m.NoteID], *m)
		if err != nil {
			return false, err
		}
		s.notes[m.NoteID] = next
		s.seq++
		s.changes = append(s.changes, app.Change{Seq: s.seq, NoteID: m.NoteID})
		if m.Kind == domain.MutationPurge {
			s.snapshots = nil
		}
	}
	s.processed[key] = true
	s.positions[consumer] = eventID
	return true, nil
}

// Changes implements app.Store.
func (s *Store) Changes(_ context.Context, after int64, limit int) ([]app.Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if after < s.prunedThrough {
		return nil, fmt.Errorf("%w: at %d, pruned through %d", app.ErrFeedGap, after, s.prunedThrough)
	}
	var out []app.Change
	for _, c := range s.changes {
		if c.Seq > after && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}

// LoadNotes implements app.Store.
func (s *Store) LoadNotes(_ context.Context, ids []string) (map[string]*domain.NoteState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*domain.NoteState{}
	for _, id := range ids {
		if n, ok := s.notes[id]; ok {
			out[id] = n.Clone()
		}
	}
	return out, nil
}

// LoadAll implements app.Store.
func (s *Store) LoadAll(_ context.Context, fn func(*domain.NoteState) error) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.notes))
	for id := range s.notes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := fn(s.notes[id].Clone()); err != nil {
			return 0, err
		}
	}
	return s.seq, nil
}

// LatestSnapshot implements app.Store.
func (s *Store) LatestSnapshot(context.Context) (app.Snapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.snapshots) == 0 {
		return app.Snapshot{}, false, nil
	}
	return s.snapshots[len(s.snapshots)-1], true, nil
}

// SaveSnapshot implements app.Store, keeping the newest two.
func (s *Store) SaveSnapshot(_ context.Context, snap app.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, snap)
	if len(s.snapshots) > 2 {
		s.snapshots = s.snapshots[len(s.snapshots)-2:]
	}
	return nil
}

// Prune drops change feed entries up to and including through, as the
// Postgres adapter's retention job does.
func (s *Store) Prune(through int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes = slices.DeleteFunc(s.changes, func(c app.Change) bool { return c.Seq <= through })
	s.prunedThrough = max(s.prunedThrough, through)
}

// SnapshotCount returns how many snapshots are stored.
func (s *Store) SnapshotCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.snapshots)
}

// LastEvent returns the last event a consumer applied.
func (s *Store) LastEvent(consumer string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.positions[consumer]
}

// Directory is an app.AccessDirectory answering from a fixed table of
// (user, workspace) -> readable notes, counting calls so tests can see the
// cache work.
type Directory struct {
	mu       sync.Mutex
	readable map[string][]string
	Calls    int
	// Err, when set, is returned by every call (identity unreachable).
	Err error
}

// NewDirectory returns an empty directory: nobody can read anything.
func NewDirectory() *Directory { return &Directory{readable: map[string][]string{}} }

// Grant sets the notes user can read in workspace.
func (d *Directory) Grant(user, workspace string, notes ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.readable[user+"|"+workspace] = slices.Clone(notes)
}

// ListReadableNotes implements app.AccessDirectory. A user with no grant in
// the workspace gets NOT_FOUND, as identity answers for non-members.
func (d *Directory) ListReadableNotes(_ context.Context, subject auth.Subject, workspace string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Calls++
	if d.Err != nil {
		return nil, d.Err
	}
	notes, ok := d.readable[subject.UserID+"|"+workspace]
	if !ok {
		return nil, &app.NotFoundError{ResourceType: "skein.v1.Workspace", Name: workspace}
	}
	return slices.Clone(notes), nil
}

// Similarity is an app.Similarity over fixed note vectors: the cosine of
// two notes' vectors.
type Similarity struct {
	Vectors map[string][]float64
	Err     error
}

// NoteCosines implements app.Similarity.
func (s *Similarity) NoteCosines(_ context.Context, _ string, pairs []app.NotePair) (map[app.NotePair]float64, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	out := map[app.NotePair]float64{}
	for _, p := range pairs {
		a, b := s.Vectors[p.From], s.Vectors[p.To]
		if a != nil && b != nil {
			out[p] = Cosine(a, b)
		}
	}
	return out, nil
}

// Cosine returns the cosine similarity of two vectors (0 if either is
// zero).
func Cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range min(len(a), len(b)) {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	c := dot / (math.Sqrt(na) * math.Sqrt(nb))
	return max(-1, min(1, c))
}

// Relevance is an app.Relevance returning fixed scores per topic.
type Relevance struct {
	Scores map[string]map[string]float64 // topic -> note -> score
	Err    error
}

// TopicRelevance implements app.Relevance.
func (r *Relevance) TopicRelevance(_ context.Context, _ string, topic string) (map[string]float64, error) {
	if r.Err != nil {
		return nil, r.Err
	}
	return r.Scores[topic], nil
}
