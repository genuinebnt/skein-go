package domain

import (
	"fmt"
	"slices"
)

// Projection is the search service's whole read model: every workspace it
// has seen, and the position in the change feed it reflects. It is
// single-threaded; the app layer guards it with a lock.
//
// The durable projection lives in the search schema (note states plus a
// change feed). A Projection follows that feed: Apply takes the current
// state of each changed note, so applying the same change twice, or
// applying a newer state over an older one, gives the same result.
type Projection struct {
	workspaces    map[string]*Workspace
	noteWorkspace map[string]string
	// Position is the sequence number of the last change applied.
	Position int64
}

// NewProjection returns an empty projection at position 0.
func NewProjection() *Projection {
	return &Projection{workspaces: map[string]*Workspace{}, noteWorkspace: map[string]string{}}
}

// Workspace returns the workspace with id.
func (p *Projection) Workspace(id string) (*Workspace, bool) {
	w, ok := p.workspaces[id]
	return w, ok
}

// WorkspaceIDs returns every workspace ID, ascending.
func (p *Projection) WorkspaceIDs() []string { return sortedKeys(p.workspaces) }

// WorkspaceOf returns the workspace a note belongs to.
func (p *Projection) WorkspaceOf(note string) (*Workspace, bool) {
	id, ok := p.noteWorkspace[note]
	if !ok {
		return nil, false
	}
	return p.workspaces[id], true
}

// Apply sets the note's state as of change-feed position seq. A nil
// state, a tombstone, or a placeholder without a workspace removes the
// note from the read model. Apply does not move Position; the caller sets
// it once a batch of changes is applied.
func (p *Projection) Apply(note string, state *NoteState, seq int64) {
	current, known := p.noteWorkspace[note]
	keep := state != nil && !state.Purged && state.WorkspaceID != ""
	if known && (!keep || current != state.WorkspaceID) {
		p.workspaces[current].remove(note, seq)
		delete(p.noteWorkspace, note)
	}
	if !keep {
		return
	}
	w := p.workspaces[state.WorkspaceID]
	if w == nil {
		w = NewWorkspace(state.WorkspaceID)
		p.workspaces[state.WorkspaceID] = w
	}
	w.put(state, seq)
	p.noteWorkspace[note] = state.WorkspaceID
}

// CheckInvariants verifies every workspace and that the note-to-workspace
// map agrees with them (P1).
func (p *Projection) CheckInvariants() error {
	count := 0
	for _, id := range p.WorkspaceIDs() {
		w := p.workspaces[id]
		for note := range w.notes {
			if p.noteWorkspace[note] != id {
				return fmt.Errorf("%w: P1: note %s of workspace %s maps to %q", ErrInvariant, note, id, p.noteWorkspace[note])
			}
			count++
		}
		if err := w.CheckInvariants(); err != nil {
			return fmt.Errorf("workspace %s: %w", id, err)
		}
	}
	if count != len(p.noteWorkspace) {
		return fmt.Errorf("%w: P1: %d notes mapped, %d held", ErrInvariant, len(p.noteWorkspace), count)
	}
	return nil
}

// States returns every note state held, by workspace then note ID, for
// snapshots and tests.
func (p *Projection) States() []*NoteState {
	var out []*NoteState
	for _, id := range p.WorkspaceIDs() {
		w := p.workspaces[id]
		notes := sortedKeys(w.notes)
		for _, n := range notes {
			out = append(out, w.notes[n].Clone())
		}
	}
	return out
}

// EqualContent reports whether two projections hold the same note states,
// ignoring index-internal document numbering.
func EqualContent(a, b *Projection) bool {
	x, y := a.States(), b.States()
	return slices.EqualFunc(x, y, func(m, n *NoteState) bool {
		return m.ID == n.ID && m.WorkspaceID == n.WorkspaceID && m.Title == n.Title && m.Trashed == n.Trashed &&
			m.Root == n.Root && slices.Equal(m.Blocks, n.Blocks) && slices.Equal(m.Links, n.Links)
	})
}
