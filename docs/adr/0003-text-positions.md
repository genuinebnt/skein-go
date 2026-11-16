# 0003. Code points inside, UTF-16 at the browser boundary

Date: 2026-11-16. Status: accepted.

## Context
Text positions can be counted in UTF-8 bytes (Go strings), Unicode code
points (Go runes, Rust chars), UTF-16 units (JavaScript and browser editors)
or grapheme clusters. Mixing units causes off-by-N bugs with emoji and
non-Latin text, and the Rust port must agree with Go.

## Decision
- Operations never carry positions, only element IDs, so the wire format does
  not depend on a unit.
- Local edit APIs in the engine count code points. Go uses `rune`, Rust uses
  `char`, and both map one to one.
- The WASM boundary converts UTF-16 offsets from the browser to code points,
  using a cached UTF-16 length per run.
- The engine is not grapheme-aware. Cursor movement over grapheme clusters is
  the editor's job.

## Consequences
- Concurrent edits can split a grapheme cluster (for example two people
  editing inside a family emoji). Every text CRDT has this property; the
  editor renders the result as separate characters.
