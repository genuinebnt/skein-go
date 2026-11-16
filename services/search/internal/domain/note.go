package domain

import (
	"fmt"
	"slices"
)

// Block is one block of a note as the projection keeps it.
type Block struct {
	ID       string
	Kind     string // "paragraph", "heading1", "code", ...
	Language string // for code blocks
	Text     string
	// Ordinal orders blocks within the note. BlocksChanged carries no
	// position (a contract gap noted in the service README), so a block
	// keeps the ordinal it got when first seen and new blocks go last.
	Ordinal int
}

// IsCode reports whether the block is a code block.
func (b Block) IsCode() bool { return b.Kind == "code" }

// NoteState is everything the projection knows about one note: the read
// model built from documents and identity events.
type NoteState struct {
	ID          string
	WorkspaceID string // empty until NoteCreated is seen
	Title       string
	Trashed     bool
	Root        bool
	// Purged marks a tombstone: the note was erased and every later event
	// for it is ignored, so a late or replayed event cannot bring erased
	// text back. A tombstone holds nothing else.
	Purged bool
	Blocks []Block  // ascending Ordinal
	Links  []string // outbound link targets, ascending, distinct, never ID
}

// Visible reports whether the note belongs in search results and the link
// graph: known workspace, not trashed, not purged.
func (n *NoteState) Visible() bool {
	return n != nil && !n.Purged && !n.Trashed && n.WorkspaceID != ""
}

// Clone returns a deep copy.
func (n *NoteState) Clone() *NoteState {
	if n == nil {
		return nil
	}
	c := *n
	c.Blocks = slices.Clone(n.Blocks)
	c.Links = slices.Clone(n.Links)
	return &c
}

// MutationKind tags the variants of Mutation.
//
// Rust: an enum with one variant per kind and only that kind's fields.
type MutationKind uint8

const (
	MutationCreate  MutationKind = 1 // NoteCreated: WorkspaceID, Title
	MutationRename  MutationKind = 2 // NoteRenamed: Title
	MutationTrash   MutationKind = 3 // NoteTrashed
	MutationRestore MutationKind = 4 // NoteRestored
	MutationPurge   MutationKind = 5 // NotePurged
	MutationBlocks  MutationKind = 6 // BlocksChanged: Blocks, RemovedBlocks
	MutationLinks   MutationKind = 7 // LinksChanged: Links (the complete set)
	// MutationRoot sets Root. No event carries it yet (the seed loader uses
	// it; see the README's contract notes).
	MutationRoot MutationKind = 8
)

// String names the kind for logs and metrics (skein.event.type).
func (k MutationKind) String() string {
	switch k {
	case MutationCreate:
		return "note_created"
	case MutationRename:
		return "note_renamed"
	case MutationTrash:
		return "note_trashed"
	case MutationRestore:
		return "note_restored"
	case MutationPurge:
		return "note_purged"
	case MutationBlocks:
		return "blocks_changed"
	case MutationLinks:
		return "links_changed"
	case MutationRoot:
		return "root_changed"
	}
	return fmt.Sprintf("MutationKind(%d)", uint8(k))
}

// Mutation is one change to one note, decoded from an event.
type Mutation struct {
	Kind          MutationKind
	NoteID        string
	WorkspaceID   string
	Title         string
	Blocks        []Block // changed or added; Ordinal is ignored
	RemovedBlocks []string
	Links         []string
	Root          bool
}

// Validate reports whether m can be applied.
func (m Mutation) Validate() error {
	if m.NoteID == "" {
		return fmt.Errorf("%w: no note id", ErrInvalidMutation)
	}
	if m.Kind < MutationCreate || m.Kind > MutationRoot {
		return fmt.Errorf("%w: kind %d", ErrInvalidMutation, m.Kind)
	}
	if m.Kind == MutationCreate && m.WorkspaceID == "" {
		return fmt.Errorf("%w: note %s created without a workspace", ErrInvalidMutation, m.NoteID)
	}
	for _, b := range m.Blocks {
		if b.ID == "" {
			return fmt.Errorf("%w: note %s has a block without an id", ErrInvalidMutation, m.NoteID)
		}
	}
	return nil
}

// ApplyMutation returns the note's state after m, given its state before
// (nil when the projection has never seen the note). It never modifies
// state. Every rule is idempotent, so a redelivered event changes nothing,
// and events for a note that arrive before its NoteCreated build a
// placeholder that stays invisible until the workspace is known.
//
// Links are normalized here, once, as libs/graph requires: self-links and
// repeats are dropped and the rest sorted.
func ApplyMutation(state *NoteState, m Mutation) (*NoteState, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if state != nil && state.Purged {
		return state.Clone(), nil
	}
	next := state.Clone()
	if next == nil {
		next = &NoteState{ID: m.NoteID}
	}
	switch m.Kind {
	case MutationCreate:
		next.WorkspaceID = m.WorkspaceID
		next.Title = m.Title
	case MutationRename:
		next.Title = m.Title
	case MutationTrash:
		next.Trashed = true
	case MutationRestore:
		next.Trashed = false
	case MutationPurge:
		return &NoteState{ID: m.NoteID, Purged: true}, nil
	case MutationBlocks:
		next.Blocks = applyBlocks(next.Blocks, m.Blocks, m.RemovedBlocks)
	case MutationLinks:
		next.Links = NormalizeLinks(m.NoteID, m.Links)
	case MutationRoot:
		next.Root = m.Root
	}
	return next, nil
}

func applyBlocks(current, changed []Block, removed []string) []Block {
	out := slices.Clone(current)
	gone := map[string]bool{}
	for _, id := range removed {
		gone[id] = true
	}
	out = slices.DeleteFunc(out, func(b Block) bool { return gone[b.ID] })
	next := 0
	for _, b := range out {
		next = max(next, b.Ordinal+1)
	}
	for _, c := range changed {
		if gone[c.ID] {
			continue
		}
		if i := slices.IndexFunc(out, func(b Block) bool { return b.ID == c.ID }); i >= 0 {
			c.Ordinal = out[i].Ordinal
			out[i] = c
			continue
		}
		c.Ordinal = next
		next++
		out = append(out, c)
	}
	return out
}

// NormalizeLinks returns targets without self-links, empty IDs and
// repeats, ascending.
func NormalizeLinks(from string, targets []string) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if t != "" && t != from {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
