# 0005. Fugue sequence with block markers

Date: 2026-11-16. Status: accepted.

## Context
Documents are Notion-like: blocks (paragraphs, headings, lists, code) with
Markdown inside each block. Two designs were considered:

1. A tree of blocks, each holding its own text sequence. Moving blocks is
   easy, but splitting a paragraph (Enter) or merging two (Backspace) becomes
   "delete text here, insert copies there", so a concurrent edit to the moved
   text is lost. Splits and merges happen constantly while typing.
2. One text sequence per note, with a marker element at the start of each
   block. Splits and merges are inserts and deletes of markers, so concurrent
   typing always survives. Automerge uses this approach.

## Decision
- One Fugue sequence per note (`libs/crdt`, spec in `docs/spec`). Fugue is
  chosen over RGA and YATA because it does not interleave concurrent typing.
- Each block starts with a marker element carrying last-writer-wins
  attributes ordered by `(lamport, replica)`: block type, indent level,
  checked state, code language. Nesting is an indent level, not a tree.
- Inline formatting is Markdown text inside the block, read by a tolerant
  parser that renders unmatched delimiters as plain text.
- Editing inside a block that is concurrently deleted: the delete wins, the
  edits land in tombstones, and undoing the delete restores both.
- Undo of a delete restores the same elements with a restore operation, so
  later edits and links that point at them still attach.

## Consequences
- Dragging a block is delete plus reinsert in phase 1, so a concurrent edit
  inside a dragged block can be lost. A move operation is a phase 3 item.
- Concurrent formatting (one person deletes `**` while another types inside
  it) changes formatting unexpectedly but never corrupts the document.
- The reference implementation is O(n) for local index lookups. An optimised
  representation will be tested against it operation for operation.
