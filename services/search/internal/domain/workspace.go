package domain

import (
	"fmt"
	"slices"

	"github.com/genuinebnt/skein-go/libs/graph"
	"github.com/genuinebnt/skein-go/libs/trigram"
)

// BlockRef names a block by note and block ID.
type BlockRef struct {
	Note, Block string
}

// idAllocator hands out small uint32 IDs, reusing freed ones smallest
// first, so bitmaps stay dense under churn and the IDs a workspace ends
// with depend only on its history.
type idAllocator struct {
	free []uint32 // ascending
	next uint32
}

func (a *idAllocator) take() uint32 {
	if len(a.free) > 0 {
		id := a.free[0]
		a.free = a.free[1:]
		return id
	}
	a.next++
	return a.next - 1
}

func (a *idAllocator) release(id uint32) {
	i, _ := slices.BinarySearch(a.free, id)
	a.free = slices.Insert(a.free, i, id)
}

// Workspace is the read model of one workspace: its notes, and the
// indexes search runs on. Only visible notes (NoteState.Visible) are
// indexed; trashed notes are kept so their IDs resolve.
//
// Invariants (CheckInvariants):
//
//   - S1 every indexed block document maps to a block of a visible note
//     with the same text, code flag and language, and back;
//   - S2 noteDocs holds exactly each visible note's block documents;
//   - S3 the title index holds exactly one document per visible note, with
//     its title;
//   - S4 the word index holds exactly the visible notes;
//   - S5 the trigram, word and bitmap structures satisfy their own
//     invariants.
type Workspace struct {
	ID    string
	notes map[string]*NoteState

	blocks   *trigram.Index
	blockDoc map[BlockRef]uint32
	blockAt  map[uint32]BlockRef
	noteDocs map[string]*trigram.Bitmap
	blockIDs idAllocator

	titles   *trigram.Index
	titleDoc map[string]uint32
	titleAt  map[uint32]string
	titleIDs idAllocator

	words *WordIndex

	// Version is the change-feed position of the last change to the
	// workspace; GraphVersion that of the last change to the link graph's
	// structure (visible notes, links, roots). Positions come from the
	// shared feed rather than a local counter, so replicas that have caught
	// up agree on them, and cache keys built from them are shared safely.
	// Both persist in snapshots.
	Version      int64
	GraphVersion int64
}

// NewWorkspace returns an empty workspace.
func NewWorkspace(id string) *Workspace {
	return &Workspace{
		ID: id, notes: map[string]*NoteState{},
		blocks: trigram.NewIndex(), blockDoc: map[BlockRef]uint32{}, blockAt: map[uint32]BlockRef{}, noteDocs: map[string]*trigram.Bitmap{},
		titles: trigram.NewIndex(), titleDoc: map[string]uint32{}, titleAt: map[uint32]string{},
		words: NewWordIndex(),
	}
}

// Note returns the note with id, if the workspace holds it (visible or
// trashed).
func (w *Workspace) Note(id string) (*NoteState, bool) {
	n, ok := w.notes[id]
	return n, ok
}

// VisibleNotes returns the IDs of the visible notes, ascending.
func (w *Workspace) VisibleNotes() []string {
	var out []string
	for id, n := range w.notes {
		if n.Visible() {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Words returns the workspace's word index (read only).
func (w *Workspace) Words() *WordIndex { return w.words }

// Blocks returns the block trigram index (read only).
func (w *Workspace) Blocks() *trigram.Index { return w.blocks }

// BlockAt returns the block behind a block document.
func (w *Workspace) BlockAt(doc uint32) (BlockRef, bool) {
	ref, ok := w.blockAt[doc]
	return ref, ok
}

// Block returns a block of a note.
func (w *Workspace) Block(ref BlockRef) (Block, bool) {
	n, ok := w.notes[ref.Note]
	if !ok {
		return Block{}, false
	}
	for _, b := range n.Blocks {
		if b.ID == ref.Block {
			return b, true
		}
	}
	return Block{}, false
}

// BlocksOf returns the block documents of the given notes as one bitmap:
// the permission filter that search intersects with its candidates.
func (w *Workspace) BlocksOf(notes []string) *trigram.Bitmap {
	out := &trigram.Bitmap{}
	for _, n := range notes {
		if docs := w.noteDocs[n]; docs != nil {
			out = trigram.Or(out, docs)
		}
	}
	return out
}

// put replaces a note's state (which belongs to this workspace), updates
// every index for the difference, and stamps the versions with seq, the
// change-feed position of the change.
func (w *Workspace) put(next *NoteState, seq int64) {
	old := w.notes[next.ID]
	wasVisible, isVisible := old.Visible(), next.Visible()
	changed := false
	switch {
	case wasVisible && !isVisible:
		w.unindex(old)
		changed = true
	case isVisible:
		changed = w.index(old, wasVisible, next)
	}
	// Links resolve by title (LinkGraph), so a rename can add or remove
	// edges into this note as well.
	if wasVisible != isVisible || isVisible && (!slices.Equal(old.Links, next.Links) || old.Root != next.Root || old.Title != next.Title) {
		w.GraphVersion = seq
		changed = true
	}
	if old == nil || old.Trashed != next.Trashed || old.Title != next.Title {
		changed = true
	}
	w.notes[next.ID] = next.Clone()
	if changed {
		w.Version = seq
	}
}

// remove drops a note entirely.
func (w *Workspace) remove(id string, seq int64) {
	old, ok := w.notes[id]
	if !ok {
		return
	}
	if old.Visible() {
		w.unindex(old)
		w.GraphVersion = seq
	}
	delete(w.notes, id)
	w.Version = seq
}

func blockDoc(b Block) trigram.Doc {
	return trigram.Doc{Text: b.Text, Code: b.IsCode(), Language: b.Language}
}

// index brings the indexes from old (indexed when wasIndexed) to next and
// reports whether anything searchable changed.
func (w *Workspace) index(old *NoteState, wasIndexed bool, next *NoteState) bool {
	previous := map[string]Block{}
	if wasIndexed {
		for _, b := range old.Blocks {
			previous[b.ID] = b
		}
	}
	changed := !wasIndexed || old.Title != next.Title
	docs := w.noteDocs[next.ID]
	if docs == nil {
		docs = &trigram.Bitmap{}
		w.noteDocs[next.ID] = docs
	}
	for _, b := range next.Blocks {
		ref := BlockRef{next.ID, b.ID}
		before, existed := previous[b.ID]
		delete(previous, b.ID)
		if existed && blockDoc(before) == blockDoc(b) {
			continue
		}
		changed = true
		if existed {
			_ = w.blocks.Update(w.blockDoc[ref], blockDoc(b)) // indexed
			continue
		}
		id := w.blockIDs.take()
		_ = w.blocks.Add(id, blockDoc(b)) // fresh id
		w.blockDoc[ref], w.blockAt[id] = id, ref
		docs.Add(id)
	}
	for _, id := range sortedKeys(previous) {
		w.dropBlock(BlockRef{next.ID, id})
		changed = true
	}
	if !wasIndexed || old.Title != next.Title {
		w.putTitle(next.ID, next.Title)
	}
	if changed {
		w.words.IndexNote(next.ID, next.Title, next.Blocks)
	}
	return changed
}

func (w *Workspace) unindex(old *NoteState) {
	for _, b := range old.Blocks {
		w.dropBlock(BlockRef{old.ID, b.ID})
	}
	delete(w.noteDocs, old.ID)
	if id, ok := w.titleDoc[old.ID]; ok {
		_ = w.titles.Remove(id)
		delete(w.titleDoc, old.ID)
		delete(w.titleAt, id)
		w.titleIDs.release(id)
	}
	w.words.RemoveNote(old.ID)
}

func (w *Workspace) dropBlock(ref BlockRef) {
	id, ok := w.blockDoc[ref]
	if !ok {
		return
	}
	_ = w.blocks.Remove(id)
	delete(w.blockDoc, ref)
	delete(w.blockAt, id)
	if docs := w.noteDocs[ref.Note]; docs != nil {
		docs.Remove(id)
	}
	w.blockIDs.release(id)
}

func (w *Workspace) putTitle(note, title string) {
	if id, ok := w.titleDoc[note]; ok {
		_ = w.titles.Update(id, trigram.Doc{Text: title})
		return
	}
	id := w.titleIDs.take()
	_ = w.titles.Add(id, trigram.Doc{Text: title})
	w.titleDoc[note], w.titleAt[id] = id, note
}

// LinkGraph builds the link graph of the visible notes that readable
// allows (nil allows all): an edge for every link between two of them. The
// graph never contains a note the caller cannot read, so nothing computed
// from it can reveal one.
//
// A note's Links are the titles inside its [[...]] (LinksChanged carries
// them as typed). They are resolved here, against the titles of the visible
// notes, exactly and within the workspace, as docs/seed-data.md defines an
// edge. Resolving when the graph is built, not when the event arrives,
// keeps edges right when the target is created, renamed or trashed later.
// Titles that match no visible note are dangling and make no edge; a title
// shared by several notes resolves to the smallest note ID, so the choice
// is deterministic.
func (w *Workspace) LinkGraph(readable func(note string) bool) (*graph.Graph, error) {
	var nodes []graph.Node
	in := map[string]bool{}
	byTitle := map[string]string{}
	for _, id := range w.VisibleNotes() {
		if _, taken := byTitle[w.notes[id].Title]; !taken {
			byTitle[w.notes[id].Title] = id
		}
		if readable == nil || readable(id) {
			nodes = append(nodes, graph.Node{ID: id, Root: w.notes[id].Root})
			in[id] = true
		}
	}
	var edges []graph.Edge
	for _, n := range nodes {
		seen := map[string]bool{}
		for _, title := range w.notes[n.ID].Links {
			to, ok := byTitle[title]
			if ok && in[to] && to != n.ID && !seen[to] {
				seen[to] = true
				edges = append(edges, graph.Edge{From: n.ID, To: to})
			}
		}
	}
	return graph.New(nodes, edges)
}

// CheckInvariants verifies S1 to S5.
func (w *Workspace) CheckInvariants() error {
	visible := w.VisibleNotes()
	wantDocs := 0
	for _, id := range visible {
		n := w.notes[id]
		docs := w.noteDocs[id]
		if docs.Len() != len(n.Blocks) {
			return fmt.Errorf("%w: S2: note %s has %d block documents for %d blocks", ErrInvariant, id, docs.Len(), len(n.Blocks))
		}
		for _, b := range n.Blocks {
			doc, ok := w.blockDoc[BlockRef{id, b.ID}]
			if !ok || !docs.Contains(doc) || w.blockAt[doc] != (BlockRef{id, b.ID}) {
				return fmt.Errorf("%w: S1: block %s/%s is not mapped", ErrInvariant, id, b.ID)
			}
			if got, ok := w.blocks.Doc(doc); !ok || got != blockDoc(b) {
				return fmt.Errorf("%w: S1: block %s/%s indexed with other content", ErrInvariant, id, b.ID)
			}
		}
		wantDocs += len(n.Blocks)
		tid, ok := w.titleDoc[id]
		if got, found := w.titles.Doc(tid); !ok || !found || got.Text != n.Title || w.titleAt[tid] != id {
			return fmt.Errorf("%w: S3: title of %s not indexed", ErrInvariant, id)
		}
		if _, ok := w.words.noteTerms[id]; !ok && (len(Tokenize(n.Title)) > 0 || hasTokens(n.Blocks)) {
			return fmt.Errorf("%w: S4: note %s not in the word index", ErrInvariant, id)
		}
	}
	if w.blocks.Len() != wantDocs || len(w.blockDoc) != wantDocs || len(w.blockAt) != wantDocs || len(w.noteDocs) != len(visible) {
		return fmt.Errorf("%w: S1/S2: %d block documents for %d blocks", ErrInvariant, w.blocks.Len(), wantDocs)
	}
	if w.titles.Len() != len(visible) || len(w.titleDoc) != len(visible) {
		return fmt.Errorf("%w: S3: %d titles for %d visible notes", ErrInvariant, w.titles.Len(), len(visible))
	}
	for note := range w.words.noteTerms {
		if n, ok := w.notes[note]; !ok || !n.Visible() {
			return fmt.Errorf("%w: S4: invisible note %s in the word index", ErrInvariant, note)
		}
	}
	if err := w.blocks.CheckInvariants(); err != nil {
		return err
	}
	if err := w.titles.CheckInvariants(); err != nil {
		return err
	}
	return w.words.CheckInvariants()
}

func hasTokens(blocks []Block) bool {
	for _, b := range blocks {
		if len(Tokenize(b.Text)) > 0 {
			return true
		}
	}
	return false
}
