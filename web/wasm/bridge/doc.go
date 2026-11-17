// Package bridge is the browser's view of a note: the small, stable API that
// ADR-0007 puts between JavaScript and the CRDT, implemented over libs/crdt
// and libs/blocks.
//
// # What it offers
//
// Apply a local edit (Insert, Delete, SetAttrs), apply remote bytes
// (ApplyRemote, ApplyRemoteAttrs), get the updates a peer lacks (OpsSince),
// and render (Blocks, RenderInline, Markdown). Everything else here is a
// convenience the editor needs on top: element IDs for cursors and presence
// (IDAt, IndexOf), history (MarkdownAt), attribution (ExportMapped), and a
// summary of a batch the server refused (DescribeOps).
//
// # Positions
//
// Every position in this package counts Unicode code points of the note's
// sequence text, block markers (U+001E) included (ADR-0003). The browser
// counts UTF-16 units; the TypeScript wrapper (web/src/crdt) converts at the
// boundary, so nothing in Go ever sees a UTF-16 offset.
//
// # Remote delivery
//
// Remote operations go through a crdt.CausalBuffer, so a network that
// reorders or repeats frames is safe: an operation that arrives before its
// dependencies waits in the buffer, and one that was already applied is a
// no-op.
//
// # Block attributes
//
// Wire format v1 of the sync protocol carries only text operations; block
// attributes (type, indent, checked, language) have no field yet. Until they
// do, SeedAttrsFromMarkdown recovers a note's attributes from the Markdown the
// server returns with GetNote, as "baseline" writes stamped (1, BaselineReplica).
// Baseline writes lose to every real write, because a real write is stamped
// above every Lamport timestamp its author has seen and every note's first
// operation already has Lamport timestamp 1.
//
// # Invariants
//
// CheckInvariants runs the sequence's I1–I7, the attribute store's A1–A3 and
// the layout's L1–L3 (see libs/crdt and libs/blocks). Tests call it after
// every operation.
//
// # Porting notes
//
// Standard library, libs/crdt and libs/blocks only: no reflection and no
// encoding/json, so the package builds with TinyGo (ADR-0007). A Rust build
// exposes the same functions through wasm-bindgen and the TypeScript wrapper
// does not change.
package bridge
