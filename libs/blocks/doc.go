// Package blocks implements Skein's block model (ADR-0005) on top of the
// sequence CRDT in libs/crdt: how a note's single text sequence is divided
// into blocks, the attributes each block carries, the inline Markdown inside
// a block, and Markdown import and export for whole notes. The normative
// description, which the Rust port must follow, is docs/spec/blocks.md.
//
// # Blocks in one sequence
//
// A note is one crdt.Sequence. Each block starts with a marker element whose
// value is Marker (U+001E); wire format v1 has no marker operation, so a
// marker is an ordinary insert (docs/seed-data.md). A block's ID is its
// marker's operation ID. Pressing Enter inserts a marker; Backspace at the
// start of a block deletes one, merging the block into the previous one. So
// splits and merges are plain inserts and deletes, and concurrent typing in
// a block that is being split or merged always survives.
//
// Elements returns every element in document order. crdt.Sequence exposes
// order only as text, so Elements replays the inserts into a scratch
// sequence with each value relabelled to a distinct code point and reads the
// order back; Fugue orders by IDs alone, so this is exact. Layout then
// splits the visible elements at markers. Text before the first marker
// (possible after its marker is deleted) forms a leading block with ID
// crdt.Root.
//
// # Attributes
//
// Block type, indent, checked state and code language are four
// last-writer-wins registers per block, kept beside the sequence in
// Attributes. A write is stamped (lamport, replica) and the larger stamp
// wins; an exact tie (only possible from a buggy client) is broken by the
// encoded value, so every replica picks the same winner in any delivery
// order. Registers do not depend on the sequence: a write may arrive before
// its block's marker and simply waits. Every distinct write is kept, so the
// attributes at an earlier version can be recomputed (Attributes.At).
//
// # Inline Markdown
//
// ParseInline reads emphasis, strong, code spans, links, [[wiki links]] and
// @mentions with a tolerant parser: it never fails, and delimiters that
// match nothing are text. Emphasis follows CommonMark's delimiter-run rules
// without the rule of three. Leaf constructs are tokenised first, left to
// right, so delimiters never cross a code span or link. The result is a
// lossless tree: its nodes cover every code point, so the source can always
// be recovered and positions map back to elements. Spans count code points.
// Parsing is linear: delimiter runs are matched through per-character lists
// and code span closers through an index of backtick runs by length.
//
// # Markdown documents
//
// Import reads a whole note; Export writes one. Export writes a canonical
// form (blank lines between blocks, single newlines inside lists, "&nbsp;"
// for an empty paragraph, a backslash before paragraph text that would read
// back as other syntax), and Import(Export(bs)) equals bs for every block
// list in the lossless subset (CheckLossless). Import accepts more than the
// canonical form and never fails, and Export(Import(md)) is a fixed point.
// ExportMapped also returns where each output code point came from, which
// blame uses.
//
// # Invariants
//
// Attributes.CheckInvariants (A1 to A3), Document.CheckInvariants (L1 to L3)
// and CheckTree (P1 to P3) verify the invariants listed in the spec. Tests
// call them after every operation. Document.CheckInvariants is O(n²) (it
// asks the sequence for the position of every element) and is for tests.
//
// # Porting notes
//
// Standard library and libs/crdt only, no I/O, no goroutines, no global
// mutable state. Inline nodes and attribute values are kind tags with
// fields (Rust enums). Positions are code points ([]rune here, Vec<char> or
// char indices in Rust). Map iteration order is never observable: Links and
// Mentions sort their output. Elements' relabelling must skip the surrogate
// range exactly as label does, or the order read back is wrong.
package blocks
