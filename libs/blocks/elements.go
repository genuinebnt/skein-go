package blocks

import (
	"fmt"
	"strings"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// Element is one element of a note's sequence: a character or a block
// marker, visible or deleted.
type Element struct {
	ID      crdt.ID
	Value   rune
	Deleted bool
}

// MaxElements is the most inserted elements Elements can order: the number
// of Unicode scalar values, because each element is relabelled with one.
const MaxElements = 0x110000 - 0x800

// Elements returns every element of the sequence built by ops (deleted ones
// included) in document order. ops must be in a causal order, such as
// crdt.Sequence.OpsSince(crdt.VersionVector{}).
//
// crdt.Sequence exposes its order only as text, so this replays the inserts
// into a scratch sequence with each value replaced by a distinct code point
// that encodes the insert's position in ops, and reads the order back from
// the scratch text. Fugue's order depends only on IDs, parents and sides,
// never on values, so the scratch order is the real one. Deletes are left
// out (so tombstones stay in the text) and inserts are renumbered densely
// per replica, keeping the replica and the relative order of counters,
// which is all sibling order compares. Time O(n) for n ops on typical
// histories, like a replay. A crdt iterator over elements would replace
// this (listed as a CRDT follow-up).
func Elements(ops []crdt.Op) ([]Element, error) {
	scratch, err := crdt.New(1)
	if err != nil {
		return nil, err
	}
	renamed := map[crdt.ID]crdt.ID{crdt.Root: crdt.Root}
	nextCounter := map[crdt.ReplicaID]uint64{}
	deleted := map[crdt.ID]bool{}
	var inserts []crdt.Op
	for _, op := range ops {
		if op.Kind == crdt.OpDelete {
			deleted[op.Target] = true
			continue
		}
		if len(inserts) == MaxElements {
			return nil, fmt.Errorf("%w: more than %d inserts", ErrTooManyElements, MaxElements)
		}
		parent, ok := renamed[op.Parent]
		if !ok {
			return nil, fmt.Errorf("%w: insert %s before its parent %s", crdt.ErrNotReady, op.ID, op.Parent)
		}
		nextCounter[op.ID.Replica]++
		id := crdt.ID{Replica: op.ID.Replica, Counter: nextCounter[op.ID.Replica]}
		renamed[op.ID] = id
		relabelled := crdt.Op{ID: id, Lamport: op.Lamport, Kind: crdt.OpInsert, Parent: parent, Side: op.Side, Value: label(len(inserts))}
		if err := scratch.Apply(relabelled); err != nil {
			return nil, fmt.Errorf("elements: replaying %s: %w", op.ID, err)
		}
		inserts = append(inserts, op)
	}
	out := make([]Element, 0, len(inserts))
	for _, r := range scratch.Text() {
		op := inserts[unlabel(r)]
		out = append(out, Element{ID: op.ID, Value: op.Value, Deleted: deleted[op.ID]})
	}
	return out, nil
}

// label maps an insert's position to a Unicode scalar value, skipping the
// surrogate range that crdt rejects.
func label(i int) rune {
	if i < 0xD800 {
		return rune(i)
	}
	return rune(i + 0x800)
}

func unlabel(r rune) int {
	if r < 0xD800 {
		return int(r)
	}
	return int(r) - 0x800
}

// Layout splits the visible elements into blocks at markers and gives each
// block its attributes. Visible text before the first marker forms a leading
// block with ID crdt.Root.
func Layout(elems []Element, attrs *Attributes) []Block {
	var out []Block
	var text strings.Builder
	var chars []crdt.ID
	open := false
	flush := func() {
		if open {
			out[len(out)-1].Text = text.String()
			out[len(out)-1].Chars = chars
		}
		text.Reset()
		chars = nil
	}
	start := func(id crdt.ID) {
		flush()
		out = append(out, Block{ID: id, Attrs: attrs.Get(id)})
		open = true
	}
	for _, e := range elems {
		if e.Deleted {
			continue
		}
		if e.Value == Marker {
			start(e.ID)
			continue
		}
		if !open {
			start(crdt.Root)
		}
		text.WriteRune(e.Value)
		chars = append(chars, e.ID)
	}
	flush()
	return out
}

// Compose lays blocks out as one note text: a Marker before each block's
// text. It is the inverse of Layout for notes without a leading block.
func Compose(bs []Block) (string, error) {
	var b strings.Builder
	for i, blk := range bs {
		if strings.ContainsRune(blk.Text, Marker) {
			return "", fmt.Errorf("%w: block %d", ErrMarkerInText, i)
		}
		b.WriteRune(Marker)
		b.WriteString(blk.Text)
	}
	return b.String(), nil
}

// Document is one replica's copy of a note: its text sequence and the block
// attributes kept beside it (ADR-0005). The sequence is exposed so the
// owner can deliver remote operations through a crdt.CausalBuffer.
//
// Not safe for concurrent use; the documents service gives each open note
// one actor goroutine that owns its Document.
type Document struct {
	seq   *crdt.Sequence
	attrs *Attributes
}

// NewDocument returns an empty note owned by replica.
func NewDocument(replica crdt.ReplicaID) (*Document, error) {
	seq, err := crdt.New(replica)
	if err != nil {
		return nil, err
	}
	return &Document{seq: seq, attrs: NewAttributes()}, nil
}

// Sequence returns the note's text sequence.
func (d *Document) Sequence() *crdt.Sequence { return d.seq }

// Attributes returns the note's attribute registers.
func (d *Document) Attributes() *Attributes { return d.attrs }

// Lamport returns the largest Lamport timestamp the document has seen, in
// text operations or attribute writes. A new attribute write is stamped one
// higher, so it beats every write its author could have seen.
func (d *Document) Lamport() uint64 { return max(d.seq.Lamport(), d.attrs.Lamport()) }

// Elements returns every element in document order (see Elements).
func (d *Document) Elements() ([]Element, error) {
	return Elements(d.seq.OpsSince(crdt.VersionVector{}))
}

// Blocks returns the note's current blocks.
func (d *Document) Blocks() ([]Block, error) {
	elems, err := d.Elements()
	if err != nil {
		return nil, err
	}
	return Layout(elems, d.attrs), nil
}

// AppendBlocks appends blocks at the end of the note as local edits: a
// marker and the text of each block, then an attribute write for every
// attribute that differs from the default. It returns the operations and
// writes to replicate; both are already applied.
//
// The writes are stamped with the sequence's Lamport clock after the
// inserts. That is safe because each register belongs to a marker created
// here, so no earlier write to it exists, and it keeps the writes visible in
// history at every version that includes the inserts.
func (d *Document) AppendBlocks(bs []Block) ([]crdt.Op, []AttrWrite, error) {
	for i, b := range bs {
		if strings.ContainsRune(b.Text, Marker) {
			return nil, nil, fmt.Errorf("%w: block %d", ErrMarkerInText, i)
		}
	}
	var ops []crdt.Op
	markers := make([]crdt.ID, len(bs))
	for i, b := range bs {
		inserted, err := d.seq.Insert(d.seq.Len(), string(Marker)+b.Text)
		ops = append(ops, inserted...)
		if err != nil {
			return ops, nil, err
		}
		markers[i] = inserted[0].ID
	}
	var writes []AttrWrite
	for i, b := range bs {
		for _, w := range d.attrs.Diff(markers[i], b.Attrs, d.seq.Lamport(), d.seq.Replica()) {
			if _, err := d.attrs.Apply(w); err != nil {
				return ops, writes, err
			}
			writes = append(writes, w)
		}
	}
	return ops, writes, nil
}

// SetAttrs changes a block's attributes as a local edit and returns the
// writes to replicate (none when nothing changes).
func (d *Document) SetAttrs(block crdt.ID, want Attrs) ([]AttrWrite, error) {
	writes := d.attrs.Diff(block, want, d.Lamport()+1, d.seq.Replica())
	for _, w := range writes {
		if _, err := d.attrs.Apply(w); err != nil {
			return nil, err
		}
	}
	return writes, nil
}

// CheckInvariants verifies the sequence, the attribute store and the
// layout (docs/spec/blocks.md section 2):
//
//   - L1 the blocks' texts, each after a marker (none before a leading
//     block), concatenate to the sequence's text;
//   - L2 only the first block may be a leading block (ID crdt.Root);
//   - L3 each block has one element ID per code point of its text, and
//     every ID, marker or character, is visible at the position the layout
//     puts it.
//
// It costs O(n²) for n elements and is meant for tests.
func (d *Document) CheckInvariants() error {
	if err := d.seq.CheckInvariants(); err != nil {
		return err
	}
	if err := d.attrs.CheckInvariants(); err != nil {
		return err
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvariant, fmt.Sprintf(format, args...))
	}
	bs, err := d.Blocks()
	if err != nil {
		return fail("layout: %v", err)
	}
	var text strings.Builder
	pos := 0
	expect := func(id crdt.ID) error {
		index, visible, ok := d.seq.IndexOf(id)
		if !ok || !visible || index != pos {
			return fail("L3: element %s expected visible at %d, found at %d (visible %v)", id, pos, index, visible)
		}
		pos++
		return nil
	}
	for i, b := range bs {
		if b.ID.IsRoot() {
			if i != 0 {
				return fail("L2: leading block at position %d", i)
			}
		} else {
			text.WriteRune(Marker)
			if err := expect(b.ID); err != nil {
				return err
			}
		}
		text.WriteString(b.Text)
		runes := []rune(b.Text)
		if len(runes) != len(b.Chars) {
			return fail("L3: block %s has %d code points and %d element ids", b.ID, len(runes), len(b.Chars))
		}
		for _, id := range b.Chars {
			if err := expect(id); err != nil {
				return err
			}
		}
	}
	if got := text.String(); got != d.seq.Text() {
		return fail("L1: layout text differs from the sequence text")
	}
	return nil
}
