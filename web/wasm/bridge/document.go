package bridge

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
)

// BaselineReplica stamps attribute writes recovered from Markdown
// (SeedAttrsFromMarkdown). No device mints it: replica IDs are random and
// this one is reserved here, and baseline writes are never sent.
const BaselineReplica crdt.ReplicaID = math.MaxUint64

// ErrBadID means a "replica:counter" string did not parse.
var ErrBadID = errors.New("bridge: malformed element id")

// Document is one replica's copy of a note in the browser. It is not safe for
// concurrent use; the browser has one thread.
type Document struct {
	doc    *blocks.Document
	buffer *crdt.CausalBuffer
}

// BlockView is one block as the editor draws it. Start is the code-point
// position of the block's first character in the note's sequence text, just
// after its marker (or 0 for a leading block).
type BlockView struct {
	ID    string
	Attrs blocks.Attrs
	Text  string
	Start int
}

// VersionEntry is one replica's position in a version vector.
type VersionEntry struct {
	Replica uint64
	Counter uint64
}

// OpsSummary describes a batch without applying it, for drafts of refused
// edits: the text it inserted, in operation order, and how many operations
// of each kind it held.
type OpsSummary struct {
	Inserted string
	Inserts  int
	Deletes  int
}

// NewDocument returns an empty note owned by replica.
func NewDocument(replica uint64) (*Document, error) {
	doc, err := blocks.NewDocument(crdt.ReplicaID(replica))
	if err != nil {
		return nil, err
	}
	return &Document{doc: doc, buffer: crdt.NewCausalBuffer(doc.Sequence(), 0)}, nil
}

// Replica returns the replica that owns this copy.
func (d *Document) Replica() uint64 { return uint64(d.doc.Sequence().Replica()) }

// ApplyRemote decodes a batch and delivers it through the causal buffer. It
// returns how many operations still wait for their dependencies.
func (d *Document) ApplyRemote(batch []byte) (pending int, err error) {
	ops, err := crdt.DecodeOps(batch)
	if err != nil {
		return d.buffer.Pending(), err
	}
	err = d.buffer.Deliver(ops...)
	return d.buffer.Pending(), err
}

// ApplyRemoteAttrs applies a batch of attribute writes. Writes commute with
// text operations, so they need no buffer.
func (d *Document) ApplyRemoteAttrs(batch []byte) error {
	writes, err := blocks.DecodeAttrs(batch)
	if err != nil {
		return err
	}
	for _, w := range writes {
		if _, err := d.doc.Attributes().Apply(w); err != nil {
			return err
		}
	}
	return nil
}

// Insert inserts text at code-point position pos and returns the encoded
// operations to send. They are already applied.
func (d *Document) Insert(pos int, text string) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, crdt.ErrInvalidText
	}
	ops, err := d.doc.Sequence().Insert(pos, text)
	if err != nil {
		return nil, err
	}
	return crdt.EncodeOps(ops), nil
}

// Delete deletes count code points from pos and returns the encoded
// operations to send. They are already applied.
func (d *Document) Delete(pos, count int) ([]byte, error) {
	if count == 0 {
		return crdt.EncodeOps(nil), nil
	}
	ops, err := d.doc.Sequence().Delete(pos, count)
	if err != nil {
		return nil, err
	}
	return crdt.EncodeOps(ops), nil
}

// SetAttrs changes a block's attributes as a local edit and returns the
// encoded writes to send (an empty batch when nothing changed).
func (d *Document) SetAttrs(blockID string, want blocks.Attrs) ([]byte, error) {
	id, err := ParseID(blockID)
	if err != nil {
		return nil, err
	}
	writes, err := d.doc.SetAttrs(id, want)
	if err != nil {
		return nil, err
	}
	return blocks.EncodeAttrs(writes), nil
}

// SeedAttrsFromMarkdown gives the note's blocks the attributes that markdown
// (the server's export at about the same version) assigns them, as baseline
// writes. Blocks are matched by position when both lists have the same
// length, and otherwise by equal text in order. It returns how many blocks
// were matched.
func (d *Document) SeedAttrsFromMarkdown(markdown string) (int, error) {
	current, err := d.doc.Blocks()
	if err != nil {
		return 0, err
	}
	if len(current) > 0 && current[0].ID.IsRoot() {
		current = current[1:]
	}
	imported := blocks.Import(markdown)
	pairs := matchBlocks(current, imported)
	for _, p := range pairs {
		for _, w := range baselineWrites(current[p[0]].ID, imported[p[1]].Attrs) {
			if _, err := d.doc.Attributes().Apply(w); err != nil {
				return 0, err
			}
		}
	}
	return len(pairs), nil
}

func matchBlocks(current, imported []blocks.Block) [][2]int {
	var pairs [][2]int
	if len(current) == len(imported) {
		for i := range current {
			pairs = append(pairs, [2]int{i, i})
		}
		return pairs
	}
	j := 0
	for i := range current {
		for k := j; k < len(imported); k++ {
			if imported[k].Text == current[i].Text {
				pairs = append(pairs, [2]int{i, k})
				j = k + 1
				break
			}
		}
	}
	return pairs
}

func baselineWrites(block crdt.ID, want blocks.Attrs) []blocks.AttrWrite {
	stamp := func(key blocks.AttrKey) blocks.AttrWrite {
		return blocks.AttrWrite{Block: block, Key: key, Lamport: 1, Replica: BaselineReplica}
	}
	def := blocks.DefaultAttrs()
	var out []blocks.AttrWrite
	if want.Type != def.Type && want.Type.Valid() {
		w := stamp(blocks.AttrType)
		w.Type = want.Type
		out = append(out, w)
	}
	if want.Indent != def.Indent {
		w := stamp(blocks.AttrIndent)
		w.Indent = want.Indent
		out = append(out, w)
	}
	if want.Checked != def.Checked {
		w := stamp(blocks.AttrChecked)
		w.Checked = want.Checked
		out = append(out, w)
	}
	if want.Language != def.Language {
		w := stamp(blocks.AttrLanguage)
		w.Language = want.Language
		out = append(out, w)
	}
	return out
}

// Text returns the note's sequence text, markers included.
func (d *Document) Text() string { return d.doc.Sequence().Text() }

// Blocks returns the note's blocks with their positions in Text.
func (d *Document) Blocks() ([]BlockView, error) {
	bs, err := d.doc.Blocks()
	if err != nil {
		return nil, err
	}
	out := make([]BlockView, len(bs))
	pos := 0
	for i, b := range bs {
		if !b.ID.IsRoot() {
			pos++ // the marker
		}
		out[i] = BlockView{ID: FormatID(b.ID), Attrs: b.Attrs, Text: b.Text, Start: pos}
		pos += utf8.RuneCountInString(b.Text)
	}
	return out, nil
}

// Version returns the version vector, sorted by replica.
func (d *Document) Version() []VersionEntry { return entriesOf(d.doc.Sequence().Version()) }

// Lamport returns the largest Lamport timestamp the note has seen.
func (d *Document) Lamport() uint64 { return d.doc.Lamport() }

// PendingRemote returns how many remote operations wait for dependencies.
func (d *Document) PendingRemote() int { return d.buffer.Pending() }

// OpsSince returns the encoded operations a peer at version v lacks.
func (d *Document) OpsSince(v []VersionEntry) []byte {
	return crdt.EncodeOps(d.doc.Sequence().OpsSince(vectorOf(v)))
}

// AllAttrs returns every attribute write the note holds, encoded.
func (d *Document) AllAttrs() []byte { return blocks.EncodeAttrs(d.doc.Attributes().Writes()) }

// IDAt returns the ID of the element at code-point position pos.
func (d *Document) IDAt(pos int) (string, error) {
	id, err := d.doc.Sequence().IDAt(pos)
	if err != nil {
		return "", err
	}
	return FormatID(id), nil
}

// IndexOf returns where the element with this ID is, or would be if it is
// deleted. ok is false for an unknown ID.
func (d *Document) IndexOf(id string) (index int, visible bool, ok bool) {
	parsed, err := ParseID(id)
	if err != nil {
		return 0, false, false
	}
	return d.doc.Sequence().IndexOf(parsed)
}

// Markdown exports the note.
func (d *Document) Markdown() (string, error) {
	bs, err := d.doc.Blocks()
	if err != nil {
		return "", err
	}
	return blocks.Export(bs), nil
}

// ExportMapped exports the note and, for each code point of the Markdown,
// the ID of the element that produced it: the character itself, or the
// block's marker for markup Export added.
func (d *Document) ExportMapped() (string, []string, error) {
	bs, err := d.doc.Blocks()
	if err != nil {
		return "", nil, err
	}
	markdown, sources := blocks.ExportMapped(bs)
	ids := make([]string, len(sources))
	for i, s := range sources {
		b := bs[s.Block]
		if s.Offset >= 0 && s.Offset < len(b.Chars) {
			ids[i] = FormatID(b.Chars[s.Offset])
		} else {
			ids[i] = FormatID(b.ID)
		}
	}
	return markdown, ids, nil
}

// MarkdownAt exports the note as it was at version v, with the attributes
// written up to the largest Lamport timestamp included (docs/spec/blocks.md
// section 3). v must be causally closed, as every version the server reports
// is.
func (d *Document) MarkdownAt(v []VersionEntry) (string, error) {
	vector := vectorOf(v)
	var included []crdt.Op
	var maxLamport uint64
	for _, op := range d.doc.Sequence().OpsSince(nil) {
		if vector.Contains(op.ID) {
			included = append(included, op)
			maxLamport = max(maxLamport, op.Lamport)
		}
	}
	elems, err := blocks.Elements(included)
	if err != nil {
		return "", err
	}
	// Baseline writes are stamped 1 and stay visible at every version.
	return blocks.Export(blocks.Layout(elems, d.doc.Attributes().At(max(maxLamport, 1)))), nil
}

// ReplaceWithMarkdown makes the note's content what markdown describes, as
// one local change: it deletes every visible element, then appends the
// imported blocks with their attributes. History uses it to restore an
// earlier version as a new one; the old elements stay as tombstones, so
// links and cursors anchored to them still resolve.
func (d *Document) ReplaceWithMarkdown(markdown string) (ops []byte, attrs []byte, err error) {
	var all []crdt.Op
	if n := utf8.RuneCountInString(d.Text()); n > 0 {
		deleted, err := d.doc.Sequence().Delete(0, n)
		if err != nil {
			return nil, nil, err
		}
		all = append(all, deleted...)
	}
	inserted, writes, err := d.doc.AppendBlocks(blocks.Import(markdown))
	if err != nil {
		return nil, nil, err
	}
	all = append(all, inserted...)
	return crdt.EncodeOps(all), blocks.EncodeAttrs(writes), nil
}

// CheckInvariants verifies the sequence, attributes and layout.
func (d *Document) CheckInvariants() error { return d.doc.CheckInvariants() }

// RenderInline parses a block's inline Markdown (docs/spec/blocks.md section 5).
func RenderInline(text string) []blocks.Node { return blocks.ParseInline(text) }

// DescribeOps summarises an encoded batch without applying it.
func DescribeOps(batch []byte) (OpsSummary, error) {
	ops, err := crdt.DecodeOps(batch)
	if err != nil {
		return OpsSummary{}, err
	}
	var s OpsSummary
	var text strings.Builder
	for _, op := range ops {
		switch op.Kind {
		case crdt.OpInsert:
			s.Inserts++
			if op.Value == blocks.Marker {
				text.WriteByte('\n')
			} else {
				text.WriteRune(op.Value)
			}
		case crdt.OpDelete:
			s.Deletes++
		}
	}
	s.Inserted = text.String()
	return s, nil
}

// ParseID parses "replica:counter" in decimal; "0:0" is the root.
func ParseID(s string) (crdt.ID, error) {
	replica, counter, found := strings.Cut(s, ":")
	if !found {
		return crdt.ID{}, fmt.Errorf("%w: %q", ErrBadID, s)
	}
	r, err := strconv.ParseUint(replica, 10, 64)
	if err != nil {
		return crdt.ID{}, fmt.Errorf("%w: %q", ErrBadID, s)
	}
	c, err := strconv.ParseUint(counter, 10, 64)
	if err != nil {
		return crdt.ID{}, fmt.Errorf("%w: %q", ErrBadID, s)
	}
	return crdt.ID{Replica: crdt.ReplicaID(r), Counter: c}, nil
}

// FormatID writes an ID as "replica:counter" in decimal, as BlockRef does.
func FormatID(id crdt.ID) string {
	return strconv.FormatUint(uint64(id.Replica), 10) + ":" + strconv.FormatUint(id.Counter, 10)
}

func entriesOf(v crdt.VersionVector) []VersionEntry {
	out := make([]VersionEntry, 0, len(v))
	for r, c := range v {
		out = append(out, VersionEntry{Replica: uint64(r), Counter: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Replica < out[j].Replica })
	return out
}

func vectorOf(entries []VersionEntry) crdt.VersionVector {
	v := crdt.VersionVector{}
	for _, e := range entries {
		v[crdt.ReplicaID(e.Replica)] = e.Counter
	}
	return v
}
