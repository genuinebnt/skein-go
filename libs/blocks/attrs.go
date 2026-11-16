package blocks

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// AttrKey names one attribute of a block. The numeric values are part of the
// attribute wire format and must not change.
type AttrKey uint8

// The attribute keys. Each is an independent last-writer-wins register per
// block, so a concurrent change of type and of indent both survive.
const (
	AttrType     AttrKey = 1
	AttrIndent   AttrKey = 2
	AttrChecked  AttrKey = 3
	AttrLanguage AttrKey = 4
)

// String returns the key's name.
func (k AttrKey) String() string {
	switch k {
	case AttrType:
		return "type"
	case AttrIndent:
		return "indent"
	case AttrChecked:
		return "checked"
	case AttrLanguage:
		return "language"
	default:
		return fmt.Sprintf("AttrKey(%d)", uint8(k))
	}
}

// AttrWrite sets one attribute of one block. Writes are values, like
// crdt.Op: created by a local change, sent as they are and stored.
//
// Only the value field for Key is meaningful; the others must be zero so
// that equal writes compare equal and encode identically.
//
// Rust: `pub struct AttrWrite { block: Id, lamport: u64, replica: ReplicaId,
// value: AttrValue }` with `enum AttrValue { Type(Type), Indent(u32),
// Checked(bool), Language(String) }`.
type AttrWrite struct {
	// Block is the ID of the block's marker element (crdt.Root for the
	// leading block).
	Block crdt.ID
	Key   AttrKey
	// Lamport and Replica form the write's stamp. Of two writes to the same
	// register, the one with the larger stamp wins (see Wins).
	Lamport uint64
	Replica crdt.ReplicaID

	Type     Type
	Indent   uint32
	Checked  bool
	Language string
}

// Validate checks the write on its own, without looking at any document.
func (w AttrWrite) Validate() error {
	if w.Replica == 0 {
		return fmt.Errorf("%w: replica 0 is reserved", ErrInvalidAttr)
	}
	if w.Lamport == 0 {
		return fmt.Errorf("%w: lamport timestamps start at 1", ErrInvalidAttr)
	}
	if !w.Block.IsRoot() && (w.Block.Replica == 0 || w.Block.Counter == 0) {
		return fmt.Errorf("%w: block %s is not an element id", ErrInvalidAttr, w.Block)
	}
	zeroType, zeroIndent, zeroChecked, zeroLanguage := w.Type == 0, w.Indent == 0, !w.Checked, w.Language == ""
	switch w.Key {
	case AttrType:
		if !w.Type.Valid() {
			return fmt.Errorf("%w: unknown block type %d", ErrInvalidAttr, w.Type)
		}
		zeroType = true
	case AttrIndent:
		if w.Indent > MaxIndent {
			return fmt.Errorf("%w: indent %d exceeds %d", ErrInvalidAttr, w.Indent, MaxIndent)
		}
		zeroIndent = true
	case AttrChecked:
		zeroChecked = true
	case AttrLanguage:
		if err := validateLanguage(w.Language); err != nil {
			return err
		}
		zeroLanguage = true
	default:
		return fmt.Errorf("%w: unknown key %d", ErrInvalidAttr, w.Key)
	}
	if !zeroType || !zeroIndent || !zeroChecked || !zeroLanguage {
		return fmt.Errorf("%w: %s write sets fields of another key", ErrInvalidAttr, w.Key)
	}
	return nil
}

// validateLanguage accepts what a Markdown fence info word can carry: no
// white space, no backticks, no control characters and no Marker, so a
// language always round-trips through Markdown export.
func validateLanguage(lang string) error {
	if len(lang) > MaxLanguageBytes || !utf8.ValidString(lang) {
		return fmt.Errorf("%w: language is too long or not UTF-8", ErrInvalidAttr)
	}
	if strings.IndexFunc(lang, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '`' || r == Marker || r == utf8.RuneError
	}) >= 0 {
		return fmt.Errorf("%w: language %q contains white space, backticks or control characters", ErrInvalidAttr, lang)
	}
	return nil
}

// Wins reports whether w beats other when both write the same register.
// Writes are ordered by (Lamport, Replica) and, for the rare tie where one
// replica wrote two different values with one timestamp (a buggy client),
// by the encoded value bytes, so every replica picks the same winner
// whatever order the writes arrive in.
func (w AttrWrite) Wins(other AttrWrite) bool {
	if w.Lamport != other.Lamport {
		return w.Lamport > other.Lamport
	}
	if w.Replica != other.Replica {
		return w.Replica > other.Replica
	}
	return bytes.Compare(appendAttrValue(nil, w), appendAttrValue(nil, other)) > 0
}

type register struct {
	block crdt.ID
	key   AttrKey
}

// Attributes holds the last-writer-wins registers of every block in a note,
// beside the note's crdt.Sequence (ADR-0005).
//
// It keeps every distinct write it has applied, not only the winners, so the
// attributes at an earlier point in history can be recomputed (At). Writes
// are few (one per formatting change), so this costs little.
//
// Registers are independent of the sequence: a write for a block whose
// marker has not arrived (or was deleted) is kept and takes effect if the
// block appears. That makes writes commutative with the text ops, so they
// need no causal ordering.
//
// Not safe for concurrent use, like crdt.Sequence.
type Attributes struct {
	writes  []AttrWrite // distinct writes in arrival order
	seen    map[AttrWrite]struct{}
	winners map[register]AttrWrite
	lamport uint64 // largest Lamport timestamp among writes
}

// NewAttributes returns an empty attribute store.
func NewAttributes() *Attributes {
	return &Attributes{seen: map[AttrWrite]struct{}{}, winners: map[register]AttrWrite{}}
}

// Apply applies one write and reports whether it changed the register's
// winner. Applying a write twice is a no-op.
func (a *Attributes) Apply(w AttrWrite) (changed bool, err error) {
	if err := w.Validate(); err != nil {
		return false, err
	}
	if _, dup := a.seen[w]; dup {
		return false, nil
	}
	a.seen[w] = struct{}{}
	a.writes = append(a.writes, w)
	if w.Lamport > a.lamport {
		a.lamport = w.Lamport
	}
	reg := register{w.Block, w.Key}
	if cur, ok := a.winners[reg]; ok && !w.Wins(cur) {
		return false, nil
	}
	a.winners[reg] = w
	return true, nil
}

// Get returns a block's attributes: the winning value of each register, or
// the default for registers never written.
func (a *Attributes) Get(block crdt.ID) Attrs {
	out := DefaultAttrs()
	if w, ok := a.winners[register{block, AttrType}]; ok {
		out.Type = w.Type
	}
	if w, ok := a.winners[register{block, AttrIndent}]; ok {
		out.Indent = w.Indent
	}
	if w, ok := a.winners[register{block, AttrChecked}]; ok {
		out.Checked = w.Checked
	}
	if w, ok := a.winners[register{block, AttrLanguage}]; ok {
		out.Language = w.Language
	}
	return out
}

// Lamport returns the largest Lamport timestamp of any applied write.
func (a *Attributes) Lamport() uint64 { return a.lamport }

// Len returns the number of distinct writes applied.
func (a *Attributes) Len() int { return len(a.writes) }

// Writes returns a copy of every distinct write in arrival order.
func (a *Attributes) Writes() []AttrWrite { return slices.Clone(a.writes) }

// At returns the attributes as they were when only the writes with a
// Lamport timestamp of at most maxLamport had been made. History uses it
// with the largest timestamp of the text operations included in a version
// (docs/spec/blocks.md section 3).
func (a *Attributes) At(maxLamport uint64) *Attributes {
	out := NewAttributes()
	for _, w := range a.writes {
		if w.Lamport <= maxLamport {
			// Writes in the store are already valid.
			_, _ = out.Apply(w)
		}
	}
	return out
}

// Diff returns the writes needed to change block's attributes from what
// they are now to want, stamped (lamport, replica). Unchanged registers get
// no write.
func (a *Attributes) Diff(block crdt.ID, want Attrs, lamport uint64, replica crdt.ReplicaID) []AttrWrite {
	cur := a.Get(block)
	var out []AttrWrite
	stamp := AttrWrite{Block: block, Lamport: lamport, Replica: replica}
	if want.Type != cur.Type {
		w := stamp
		w.Key, w.Type = AttrType, want.Type
		out = append(out, w)
	}
	if want.Indent != cur.Indent {
		w := stamp
		w.Key, w.Indent = AttrIndent, want.Indent
		out = append(out, w)
	}
	if want.Checked != cur.Checked {
		w := stamp
		w.Key, w.Checked = AttrChecked, want.Checked
		out = append(out, w)
	}
	if want.Language != cur.Language {
		w := stamp
		w.Key, w.Language = AttrLanguage, want.Language
		out = append(out, w)
	}
	return out
}

// CheckInvariants verifies the store (docs/spec/blocks.md section 3):
//
//   - A1 every stored write is valid and distinct, and seen indexes exactly
//     the stored writes;
//   - A2 each register's winner is the maximum, under Wins, of the writes to
//     it, and every register with a write has a winner;
//   - A3 the Lamport clock is the largest stored timestamp.
func (a *Attributes) CheckInvariants() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvariant, fmt.Sprintf(format, args...))
	}
	if len(a.seen) != len(a.writes) {
		return fail("A1: %d indexed writes for %d stored", len(a.seen), len(a.writes))
	}
	best := map[register]AttrWrite{}
	var lamport uint64
	for _, w := range a.writes {
		if err := w.Validate(); err != nil {
			return fail("A1: stored write invalid: %v", err)
		}
		if _, ok := a.seen[w]; !ok {
			return fail("A1: write for %s not indexed", w.Block)
		}
		reg := register{w.Block, w.Key}
		if cur, ok := best[reg]; !ok || w.Wins(cur) {
			best[reg] = w
		}
		lamport = max(lamport, w.Lamport)
	}
	if len(best) != len(a.winners) {
		return fail("A2: %d registers written, %d winners", len(best), len(a.winners))
	}
	for reg, w := range best {
		if a.winners[reg] != w {
			return fail("A2: register %s/%s has the wrong winner", reg.block, reg.key)
		}
	}
	if lamport != a.lamport {
		return fail("A3: clock %d, largest stored timestamp %d", a.lamport, lamport)
	}
	return nil
}
