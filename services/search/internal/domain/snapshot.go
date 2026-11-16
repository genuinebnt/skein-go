package domain

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/genuinebnt/skein-go/libs/trigram"
)

// Snapshot format. Integers are little-endian, lengths and counts minimal
// unsigned LEB128 varints, strings a varint length then bytes:
//
//	"SKSP", u32 version 1, i64 position, varint workspace count
//	per workspace (ascending ID):
//	    string id, u64 version, u64 graph version, varint note count
//	    per note (ascending ID):
//	        string id, u8 flags (bit 0 trashed, bit 1 root), string title,
//	        varint link count, strings
//	        varint block count, per block: string id, string kind,
//	        string language, string text, varint ordinal
//	    varint block document count, per document (ascending):
//	        u32 document, string note, string block
//	    varint next document, varint free count, u32 free documents
//	    varint length, the block trigram index (libs/trigram snapshot)
//
// The trigram index, by far the most expensive part to rebuild, is stored
// whole; the title and word indexes are rebuilt from the notes on load.
const (
	snapshotMagic   = "SKSP"
	snapshotVersion = 1
)

// EncodeSnapshot serializes the projection.
func EncodeSnapshot(p *Projection) ([]byte, error) {
	e := &encoder{}
	e.raw([]byte(snapshotMagic))
	e.u32(snapshotVersion)
	e.u64(uint64(p.Position))
	ids := p.WorkspaceIDs()
	e.uvarint(uint64(len(ids)))
	for _, id := range ids {
		w := p.workspaces[id]
		e.str(id)
		e.u64(uint64(w.Version))
		e.u64(uint64(w.GraphVersion))
		notes := sortedKeys(w.notes)
		e.uvarint(uint64(len(notes)))
		for _, nid := range notes {
			encodeNote(e, w.notes[nid])
		}
		docs := make([]uint32, 0, len(w.blockAt))
		for d := range w.blockAt {
			docs = append(docs, d)
		}
		slices.Sort(docs)
		e.uvarint(uint64(len(docs)))
		for _, d := range docs {
			e.u32(d)
			e.str(w.blockAt[d].Note)
			e.str(w.blockAt[d].Block)
		}
		e.uvarint(uint64(w.blockIDs.next))
		e.uvarint(uint64(len(w.blockIDs.free)))
		for _, f := range w.blockIDs.free {
			e.u32(f)
		}
		index, err := w.blocks.MarshalBinary()
		if err != nil {
			return nil, err
		}
		e.uvarint(uint64(len(index)))
		e.raw(index)
	}
	return e.buf, nil
}

func encodeNote(e *encoder, n *NoteState) {
	e.str(n.ID)
	var flags byte
	if n.Trashed {
		flags |= 1
	}
	if n.Root {
		flags |= 2
	}
	e.raw([]byte{flags})
	e.str(n.Title)
	e.uvarint(uint64(len(n.Links)))
	for _, l := range n.Links {
		e.str(l)
	}
	e.uvarint(uint64(len(n.Blocks)))
	for _, b := range n.Blocks {
		e.str(b.ID)
		e.str(b.Kind)
		e.str(b.Language)
		e.str(b.Text)
		e.uvarint(uint64(b.Ordinal))
	}
}

// DecodeSnapshot restores a projection. It checks that the stored trigram
// index and document map agree with the notes (every visible block mapped
// to a document with its exact content), which is far cheaper than
// re-extracting trigrams; the trigram postings themselves are trusted, as
// the service wrote them. Tests run the full CheckInvariants after decode.
func DecodeSnapshot(data []byte) (*Projection, error) {
	d := &decoder{data: data}
	if string(d.raw(4)) != snapshotMagic || d.u32() != snapshotVersion {
		return nil, fmt.Errorf("%w: bad header", ErrCorruptSnapshot)
	}
	p := NewProjection()
	p.Position = int64(d.u64())
	count := d.uvarint()
	for range count {
		if d.err != nil {
			break
		}
		w, err := decodeWorkspace(d)
		if err != nil {
			return nil, err
		}
		if _, dup := p.workspaces[w.ID]; dup {
			return nil, fmt.Errorf("%w: workspace %s twice", ErrCorruptSnapshot, w.ID)
		}
		p.workspaces[w.ID] = w
		for note := range w.notes {
			if _, dup := p.noteWorkspace[note]; dup {
				return nil, fmt.Errorf("%w: note %s in two workspaces", ErrCorruptSnapshot, note)
			}
			p.noteWorkspace[note] = w.ID
		}
	}
	if d.err != nil {
		return nil, d.err
	}
	if len(d.data) != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrCorruptSnapshot, len(d.data))
	}
	return p, nil
}

func decodeWorkspace(d *decoder) (*Workspace, error) {
	w := NewWorkspace(d.str())
	w.Version = int64(d.u64())
	w.GraphVersion = int64(d.u64())
	notes := d.uvarint()
	for range notes {
		if d.err != nil {
			return nil, d.err
		}
		n := &NoteState{ID: d.str(), WorkspaceID: w.ID}
		flags := d.raw(1)
		if len(flags) == 1 {
			n.Trashed, n.Root = flags[0]&1 != 0, flags[0]&2 != 0
		}
		n.Title = d.str()
		for range d.uvarint() {
			if d.err != nil {
				break
			}
			n.Links = append(n.Links, d.str())
		}
		for range d.uvarint() {
			if d.err != nil {
				break
			}
			n.Blocks = append(n.Blocks, Block{ID: d.str(), Kind: d.str(), Language: d.str(), Text: d.str(), Ordinal: int(d.uvarint())})
		}
		w.notes[n.ID] = n
	}
	docs := d.uvarint()
	for range docs {
		if d.err != nil {
			return nil, d.err
		}
		doc := d.u32()
		ref := BlockRef{Note: d.str(), Block: d.str()}
		if _, dup := w.blockAt[doc]; dup {
			return nil, fmt.Errorf("%w: document %d twice", ErrCorruptSnapshot, doc)
		}
		w.blockAt[doc], w.blockDoc[ref] = ref, doc
		if w.noteDocs[ref.Note] == nil {
			w.noteDocs[ref.Note] = &trigram.Bitmap{}
		}
		w.noteDocs[ref.Note].Add(doc)
	}
	w.blockIDs.next = uint32(d.uvarint())
	for range d.uvarint() {
		if d.err != nil {
			break
		}
		w.blockIDs.free = append(w.blockIDs.free, d.u32())
	}
	index := d.raw(int(d.uvarint()))
	if d.err != nil {
		return nil, d.err
	}
	blocks, err := trigram.UnmarshalIndex(index)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace %s: %v", ErrCorruptSnapshot, w.ID, err)
	}
	w.blocks = blocks
	for _, id := range w.VisibleNotes() {
		n := w.notes[id]
		w.putTitle(id, n.Title)
		w.words.IndexNote(id, n.Title, n.Blocks)
		if w.noteDocs[id] == nil {
			w.noteDocs[id] = &trigram.Bitmap{}
		}
	}
	return w, w.checkDocumentMap()
}

// checkDocumentMap verifies S1 and S2 without re-extracting trigrams.
func (w *Workspace) checkDocumentMap() error {
	want := 0
	for _, id := range w.VisibleNotes() {
		n := w.notes[id]
		want += len(n.Blocks)
		if w.noteDocs[id].Len() != len(n.Blocks) {
			return fmt.Errorf("%w: note %s has %d documents for %d blocks", ErrCorruptSnapshot, id, w.noteDocs[id].Len(), len(n.Blocks))
		}
		for _, b := range n.Blocks {
			doc, ok := w.blockDoc[BlockRef{id, b.ID}]
			got, found := w.blocks.Doc(doc)
			if !ok || !found || got != blockDoc(b) {
				return fmt.Errorf("%w: block %s/%s does not match its document", ErrCorruptSnapshot, id, b.ID)
			}
		}
	}
	if want != len(w.blockAt) || want != w.blocks.Len() || len(w.noteDocs) != len(w.VisibleNotes()) {
		return fmt.Errorf("%w: workspace %s maps %d documents for %d blocks", ErrCorruptSnapshot, w.ID, len(w.blockAt), want)
	}
	for _, f := range w.blockIDs.free {
		if _, used := w.blockAt[f]; used || f >= w.blockIDs.next {
			return fmt.Errorf("%w: free document %d is in use or unallocated", ErrCorruptSnapshot, f)
		}
	}
	for d := range w.blockAt {
		if d >= w.blockIDs.next {
			return fmt.Errorf("%w: document %d beyond the allocator", ErrCorruptSnapshot, d)
		}
	}
	return nil
}

type encoder struct{ buf []byte }

func (e *encoder) raw(b []byte)     { e.buf = append(e.buf, b...) }
func (e *encoder) u32(v uint32)     { e.buf = binary.LittleEndian.AppendUint32(e.buf, v) }
func (e *encoder) u64(v uint64)     { e.buf = binary.LittleEndian.AppendUint64(e.buf, v) }
func (e *encoder) uvarint(v uint64) { e.buf = binary.AppendUvarint(e.buf, v) }
func (e *encoder) str(s string)     { e.uvarint(uint64(len(s))); e.buf = append(e.buf, s...) }

type decoder struct {
	data []byte
	err  error
}

func (d *decoder) fail() {
	if d.err == nil {
		d.err = fmt.Errorf("%w: truncated", ErrCorruptSnapshot)
	}
	d.data = nil
}

func (d *decoder) raw(n int) []byte {
	if d.err != nil || n < 0 || len(d.data) < n {
		d.fail()
		return nil
	}
	out := d.data[:n]
	d.data = d.data[n:]
	return out
}

func (d *decoder) u32() uint32 {
	if b := d.raw(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (d *decoder) u64() uint64 {
	if b := d.raw(8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}

func (d *decoder) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.data)
	if n <= 0 || n != len(binary.AppendUvarint(nil, v)) || v > 1<<40 {
		d.fail()
		return 0
	}
	d.data = d.data[n:]
	return v
}

func (d *decoder) str() string {
	n := d.uvarint()
	if n > uint64(len(d.data)) {
		d.fail()
		return ""
	}
	return string(d.raw(int(n)))
}
