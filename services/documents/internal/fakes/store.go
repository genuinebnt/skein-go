// Package fakes holds in-memory implementations of the documents service's
// ports, for tests that need no Docker, network or other service
// (ADR-0013). They follow the same contracts as the real adapters,
// including the transactional outbox (an event is recorded if and only if
// its change is) and conflict detection on the op log, and the store can
// inject failures to test crash safety.
package fakes

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

// Store is an in-memory ports.Store.
type Store struct {
	mu          sync.Mutex
	notes       map[domain.NoteID]domain.Note
	purged      map[domain.NoteID]bool
	batches     map[domain.NoteID][]domain.Batch
	snapshots   map[domain.NoteID][]domain.Snapshot
	projections map[domain.NoteID]domain.Projection
	outbox      []domain.Event

	// FailAppend, when set, is called before each AppendBatches; a non-nil
	// result fails the call and nothing is written, as a rolled-back
	// transaction.
	FailAppend func(id domain.NoteID, batches []domain.Batch) error
	// FailPublish does the same for PublishProjection.
	FailPublish func(id domain.NoteID) error
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		notes: map[domain.NoteID]domain.Note{}, purged: map[domain.NoteID]bool{},
		batches: map[domain.NoteID][]domain.Batch{}, snapshots: map[domain.NoteID][]domain.Snapshot{},
		projections: map[domain.NoteID]domain.Projection{},
	}
}

var _ ports.Store = (*Store)(nil)

// Outbox returns every event committed so far, in commit order.
func (s *Store) Outbox() []domain.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.outbox)
}

// Batches returns a note's op log with payloads.
func (s *Store) Batches(id domain.NoteID) []domain.Batch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.batches[id])
}

// Snapshots returns how many snapshots a note has.
func (s *Store) Snapshots(id domain.NoteID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.snapshots[id])
}

// InsertNote implements ports.Store.
func (s *Store) InsertNote(_ context.Context, note domain.Note, first *domain.Batch, p domain.Projection, events []domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.notes[note.ID]; ok || s.purged[note.ID] {
		return fmt.Errorf("%w: note %s", domain.ErrAlreadyExists, note.ID)
	}
	s.notes[note.ID] = note
	if first != nil {
		s.batches[note.ID] = []domain.Batch{*first}
	}
	s.projections[note.ID] = cloneProjection(p)
	s.outbox = append(s.outbox, events...)
	return nil
}

// GetNote implements ports.Store.
func (s *Store) GetNote(_ context.Context, id domain.NoteID) (domain.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notes[id]
	if !ok {
		return n, fmt.Errorf("%w: note %s", domain.ErrNotFound, id)
	}
	return n, nil
}

// ListNotes implements ports.Store.
func (s *Store) ListNotes(_ context.Context, q ports.NoteQuery) ([]domain.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := slices.Clone(q.IDs)
	slices.Sort(ids)
	var out []domain.Note
	for _, id := range slices.Compact(ids) {
		n, ok := s.notes[id]
		if !ok || n.WorkspaceID != q.Workspace || id <= q.After || (n.Trashed() && !q.IncludeTrashed) {
			continue
		}
		out = append(out, n)
		if len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

// UpdateNote implements ports.Store.
func (s *Store) UpdateNote(_ context.Context, note domain.Note, events []domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.notes[note.ID]; !ok {
		return fmt.Errorf("%w: note %s", domain.ErrNotFound, note.ID)
	}
	s.notes[note.ID] = note
	s.outbox = append(s.outbox, events...)
	return nil
}

// AppendBatches implements ports.Store.
func (s *Store) AppendBatches(_ context.Context, id domain.NoteID, batches []domain.Batch, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailAppend != nil {
		if err := s.FailAppend(id, batches); err != nil {
			return err
		}
	}
	n, ok := s.notes[id]
	if !ok {
		return fmt.Errorf("%w: note %s", domain.ErrNotFound, id)
	}
	log := s.batches[id]
	for i, b := range batches {
		if err := b.Validate(); err != nil {
			return err
		}
		if b.Seq != int64(len(log)+i+1) {
			return fmt.Errorf("%w: batch %d, log has %d", domain.ErrConflict, b.Seq, len(log)+i)
		}
	}
	s.batches[id] = append(log, batches...)
	n.UpdatedAt = updatedAt
	s.notes[id] = n
	return nil
}

// LoadBatches implements ports.Store.
func (s *Store) LoadBatches(_ context.Context, id domain.NoteID, payloadsAfter int64) ([]domain.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.batches[id])
	for i := range out {
		if out[i].Seq <= payloadsAfter {
			out[i] = out[i].Meta()
		}
	}
	return out, nil
}

// LoadBatch implements ports.Store.
func (s *Store) LoadBatch(_ context.Context, id domain.NoteID, seq int64) (domain.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.batches[id]
	if seq < 1 || seq > int64(len(log)) {
		return domain.Batch{}, fmt.Errorf("%w: batch %d of note %s", domain.ErrNotFound, seq, id)
	}
	return log[seq-1], nil
}

// SaveSnapshot implements ports.Store.
func (s *Store) SaveSnapshot(_ context.Context, id domain.NoteID, snap domain.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[id] = append(s.snapshots[id], snap)
	return nil
}

// LatestSnapshot implements ports.Store.
func (s *Store) LatestSnapshot(_ context.Context, id domain.NoteID) (domain.Snapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best domain.Snapshot
	found := false
	for _, snap := range s.snapshots[id] {
		if !found || snap.ThroughSeq > best.ThroughSeq {
			best, found = snap, true
		}
	}
	return best, found, nil
}

// LoadProjection implements ports.Store.
func (s *Store) LoadProjection(_ context.Context, id domain.NoteID) (domain.Projection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjection(s.projections[id]), nil
}

// PublishProjection implements ports.Store.
func (s *Store) PublishProjection(_ context.Context, id domain.NoteID, p domain.Projection, events []domain.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailPublish != nil {
		if err := s.FailPublish(id); err != nil {
			return false, err
		}
	}
	if cur, ok := s.projections[id]; ok && cur.ThroughSeq > p.ThroughSeq {
		return false, nil
	}
	s.projections[id] = cloneProjection(p)
	s.outbox = append(s.outbox, events...)
	return true, nil
}

// StaleProjections implements ports.Store.
func (s *Store) StaleProjections(_ context.Context, limit int) ([]domain.NoteID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.NoteID
	for id, log := range s.batches {
		if len(log) > 0 && s.projections[id].ThroughSeq < log[len(log)-1].Seq && !s.purged[id] {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out[:min(limit, len(out))], nil
}

// TrashedBefore implements ports.Store.
func (s *Store) TrashedBefore(_ context.Context, t time.Time, limit int) ([]domain.NoteID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.NoteID
	for id, n := range s.notes {
		if n.Trashed() && n.TrashedAt.Before(t) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out[:min(limit, len(out))], nil
}

// PurgeNote implements ports.Store.
func (s *Store) PurgeNote(_ context.Context, id domain.NoteID, events []domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.notes, id)
	delete(s.batches, id)
	delete(s.snapshots, id)
	delete(s.projections, id)
	s.purged[id] = true
	s.outbox = append(s.outbox, events...)
	return nil
}

func cloneProjection(p domain.Projection) domain.Projection {
	p.Digests = maps.Clone(p.Digests)
	p.Links = slices.Clone(p.Links)
	return p
}
