package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"

	"github.com/genuinebnt/skein-go/libs/blocks"
)

// EventKind tags a domain event; each maps to one payload of skein.v1.Event.
type EventKind uint8

// The events documents publishes to documents.events (ordering key: note ID).
const (
	EventNoteCreated   EventKind = 1
	EventNoteRenamed   EventKind = 2
	EventNoteTrashed   EventKind = 3
	EventNoteRestored  EventKind = 4
	EventNotePurged    EventKind = 5
	EventBlocksChanged EventKind = 6
	EventLinksChanged  EventKind = 7
)

// String returns the payload name used in metrics (skein.event.type).
func (k EventKind) String() string {
	switch k {
	case EventNoteCreated:
		return "note_created"
	case EventNoteRenamed:
		return "note_renamed"
	case EventNoteTrashed:
		return "note_trashed"
	case EventNoteRestored:
		return "note_restored"
	case EventNotePurged:
		return "note_purged"
	case EventBlocksChanged:
		return "blocks_changed"
	case EventLinksChanged:
		return "links_changed"
	}
	return "unknown"
}

// ChangedBlock is the current state of one block, so projections can
// re-index without calling back.
type ChangedBlock struct {
	BlockID  string
	Kind     string
	Language string
	Text     string
}

// Event is a domain event, written to the outbox in the same transaction as
// the change it reports. Only the fields of its Kind are set.
type Event struct {
	Kind        EventKind
	NoteID      NoteID
	WorkspaceID WorkspaceID
	Actor       Subject
	At          time.Time

	Title           string         // created, renamed
	Blocks          []ChangedBlock // blocks changed
	RemovedBlockIDs []string       // blocks changed
	Links           []string       // links changed: the complete set
}

// blockDigest identifies what a projection indexes about a block.
func blockDigest(b blocks.Block) string {
	h := sha256.New()
	h.Write([]byte(b.Type.String()))
	h.Write([]byte{0})
	h.Write([]byte(b.Language))
	h.Write([]byte{0})
	h.Write([]byte(b.Text))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// NextProjection compares a note's current blocks with what was last
// published and returns the new projection state and the events that
// bring consumers up to date: BlocksChanged with every new or changed block
// and every removed one, and LinksChanged with the complete link set when
// it changed. Either event is omitted when it would be empty.
func NextProjection(note Note, old Projection, bs []blocks.Block, throughSeq int64, actor Subject, at time.Time) (Projection, []Event) {
	next := Projection{ThroughSeq: throughSeq, Digests: make(map[string]string, len(bs)), Links: blocks.Links(bs)}
	var changed []ChangedBlock
	for _, b := range bs {
		id, digest := b.ID.String(), blockDigest(b)
		next.Digests[id] = digest
		if old.Digests[id] != digest {
			changed = append(changed, ChangedBlock{BlockID: id, Kind: b.Type.String(), Language: b.Language, Text: b.Text})
		}
	}
	var removed []string
	for id := range old.Digests {
		if _, ok := next.Digests[id]; !ok {
			removed = append(removed, id)
		}
	}
	slices.Sort(removed)
	var events []Event
	base := Event{NoteID: note.ID, WorkspaceID: note.WorkspaceID, Actor: actor, At: at}
	if len(changed) > 0 || len(removed) > 0 {
		e := base
		e.Kind, e.Blocks, e.RemovedBlockIDs = EventBlocksChanged, changed, removed
		events = append(events, e)
	}
	if !slices.Equal(old.Links, next.Links) {
		e := base
		e.Kind, e.Links = EventLinksChanged, next.Links
		events = append(events, e)
	}
	return next, events
}
