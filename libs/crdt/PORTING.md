# Porting `libs/crdt` to Rust (`skein-crdt`)

The replicated text sequence every note is made of. This is the first
library to port: everything else that edits text builds on it, and the
browser runs it as WebAssembly.

Normative spec: [`docs/spec/crdt-sequence.md`](../../docs/spec/crdt-sequence.md).
Decisions: [ADR-0003](../../docs/adr/0003-text-positions.md) (code points),
[ADR-0005](../../docs/adr/0005-sequence-crdt.md) (Fugue with block markers),
[ADR-0007](../../docs/adr/0007-offline-wasm-client.md) (WASM client),
[ADR-0012](../../docs/adr/0012-rust-portable-go.md) (Go written to port).
General rules: [`docs/porting-to-rust.md`](../../docs/porting-to-rust.md).
A worked example of the insert rule, by hand, is in
[`docs/porting/libs/crdt/fugue-by-hand.md`](../../docs/porting/libs/crdt/fugue-by-hand.md).

## 1. What it is and why Skein needs it

`crdt` is a list CRDT: any number of replicas edit the same text, online or
offline, and every replica that has applied the same set of operations shows
exactly the same text, whatever order the operations arrived in. The
algorithm is Fugue (Weidner and Kleppmann). Every character is a node in a
tree, either a left or a right child of its parent; the text is the in-order
traversal, with siblings ordered by ID. Fugue was chosen over RGA and YATA
because it does not interleave two people's concurrent typing at the same
spot.

Skein needs it because a note is one sequence (ADR-0005): typing, Enter
(insert a block marker) and Backspace at a block start (delete one) are all
sequence operations, so concurrent edits always merge. The binary batch
format (spec section 7) is the sync wire format and the op-log storage
format.

Where it is used:

| Consumer | What it uses | Feature |
|---|---|---|
| `services/documents` (`internal/domain/batch.go`, `undo.go`, `history.go`; `internal/app/session.go`, `state.go`, `history.go`) | `Sequence`, `Op`, `ID`, `VersionVector`, `CausalBuffer`, `EncodeOps`/`DecodeOps` | `ApplyOps`, `Sync` (live stream), `GetNote`, `ListVersions`, `GetTextAt`, `Diff`, `Blame`, `Undo`, `Redo` in `documents.proto`; the op log and snapshots in Postgres (`internal/adapters/postgres/store.go`); the cached state in Redis |
| `libs/blocks` | `Sequence`, `Op`, `ID`, the clock | blocks, attributes, Markdown, `Elements` replay |
| `web/wasm/bridge` | everything above through `blocks.Document` and `CausalBuffer` | the browser's offline replica: local edits, remote bytes, `OpsSince`, cursors (`IDAt`, `IndexOf`) |
| `tools/seed` | `Sequence`, `EncodeOps` | generating the committed seed histories and conflicts |

The browser path matters most for the port: the TypeScript wrapper
(`web/src/crdt/wasm.ts`) calls a fixed set of functions, and a Rust WASM
build can replace the Go one behind them (ADR-0007).

## 2. Learn first

Read in this order. Effort is focused reading time.

| # | Source | What to take from it | Maps to | Time |
|---|---|---|---|---|
| 1 | Martin Kleppmann, ["CRDTs: The Hard Parts"](https://martin.kleppmann.com/2020/07/06/crdt-hard-parts-hydra.html) (Hydra 2020, [video](https://www.youtube.com/watch?v=PMVBuMK_pJY)) | The mental model: operation-based CRDTs, why list CRDTs give each character an ID, the interleaving anomaly, and why tombstones stay | `doc.go`, `sequence.go` | 1 h |
| 2 | Kleppmann, Gomes, Mulligan, Beresford, ["Interleaving anomalies in collaborative text editors"](https://martin.kleppmann.com/2019/03/25/papoc-interleaving-anomalies.html) (PaPoC 2019) | The exact anomaly Fugue avoids, with examples for Logoot, LSEQ and RGA | `convergence_test.go` (`...DoesNotInterleave`) | 0.5 h |
| 3 | Matthew Weidner and Martin Kleppmann, ["The Art of the Fugue: Minimizing Interleaving in Collaborative Text Editing"](https://arxiv.org/abs/2305.00583) (arXiv 2305.00583; IEEE TPDS 36(11), 2025, [summary](https://martin.kleppmann.com/2025/11/01/fugue-minimizing-interleaving.html)) | The tree, the in-order traversal, the two-case insert rule (right child of L, or left child of the next element R) and the maximal non-interleaving proof. Skein implements the tree form with siblings sorted by ID | `sequence.go` (`newInsert`, `applyInsert`, `insertSorted`), spec sections 3 and 4 | 3 h |
| 4 | [mweidner037/fugue](https://github.com/mweidner037/fugue) on GitHub | The authors' TypeScript reference; read the tree insert and traversal and compare with `sequence.go` | `sequence.go` | 1 h |
| 5 | Leslie Lamport, ["Time, Clocks, and the Ordering of Events in a Distributed System"](https://amturing.acm.org/p558-lamport.pdf) (CACM 21(7), 1978) | Logical clocks: a local event ticks the clock, a received timestamp raises it. Skein's `lamport` field is exactly IR1 and IR2; the sequence does not order by it, the block attributes do | `Sequence.Lamport`, `record`, `newInsert` | 1 h |
| 6 | Parker et al., ["Detection of Mutual Inconsistency in Distributed Systems"](https://www.cs.purdue.edu/homes/bb/cs542-16Spr/Parker_TSE83.pdf) (IEEE TSE SE-9(3), 1983) | Version vectors. Skein's vector is one counter per replica; because delivery is causal, "op `(r, c)` applied" is just `c <= V[r]` | `version.go`, `OpsSince`, spec section 6 | 0.5 h |
| 7 | Shapiro, Preguiça, Baquero, Zawirski, ["A comprehensive study of Convergent and Commutative Replicated Data Types"](https://webarchive.di.uminho.pt/haslab.uminho.pt/cbm/publications/comprehensive-study-convergent-and-commutative-replicated-data-types.html) (INRIA RR-7506, 2011) | Vocabulary: op-based versus state-based, causal delivery as a precondition, the LWW register (used by `libs/blocks`) | `causal.go`, `Apply` | 1 h (skim) |
| 8 | Joseph Gentle, ["5000x faster CRDTs: An Adventure in Optimization"](https://josephg.com/blog/crdts-go-brrr) | Why this reference implementation is O(n) per local edit and what the optimised one will need: run-length encoded items and a B-tree whose inner nodes store counts | `doc.go` "Representation"; the planned optimised `Sequence` | 1 h |
| 9 | [josephg/diamond-types](https://github.com/josephg/diamond-types) (Rust) | A production Rust text CRDT: how it lays out runs, IDs and a range tree, and how it is compiled to WASM | later optimisation, not the first port | browse |
| 10 | [Loro](https://loro.dev/blog/v1.0) ([loro-dev/loro](https://github.com/loro-dev/loro/)) | A Rust CRDT library whose text type is based on Fugue, with Eg-walker ideas and Peritext-style rich text; compare its Fugue with yours once yours passes the fixtures | `sequence.go` | browse |
| 11 | [Automerge 2](https://automerge.org/blog/automerge-2/) (Rust core compiled to WASM) and [Yjs](https://github.com/yjs/yjs/) (YATA; Rust port y-crdt/yrs) | How mature CRDTs ship a Rust core to the browser behind a JavaScript API; Automerge's block markers are the model for `libs/blocks` | the WASM bridge, ADR-0007 | browse |
| 12 | Gentle and Kleppmann, ["Collaborative Text Editing with Eg-walker"](https://arxiv.org/abs/2409.14252) (EuroSys 2025) | Optional: the next step after a tree CRDT, replaying an event graph instead of keeping the tree in memory | future work | 1 h (optional) |
| 13 | [josephg/editing-traces](https://www.github.com/josephg/editing-traces) | Real keystroke traces (sequential and concurrent) for criterion benchmarks of the Rust port | `bench_test.go` | when benchmarking |

Total: about 10 hours of reading before code, plus browsing.

## 3. Port order

Port the crate docs from `doc.go` first (as `//!` in `lib.rs`), then the
files in this order. "Needs" lists the earlier files a file depends on.

| # | Go file | Contents | Needs | Invariants it owns | Done when |
|---|---|---|---|---|---|
| 1 | `doc.go` | package docs: algorithm, representation, invariants I1–I7, causality, positions, porting notes | none | documents I1–I7 | `lib.rs` `//!` docs carry the same sections |
| 2 | `errors.go` | `ErrInvalidOp`, `ErrNotReady`, `ErrIDConflict`, `ErrIndexOutOfRange`, `ErrInvalidText`, `ErrInvalidReplica`, `ErrBufferFull`, `ErrCorrupt`, `ErrInvariant` | none | none | the `Error` enum in section 5 compiles; tests use `matches!` |
| 3 | `id.go` | `ReplicaID`, `ID` (`replica`, `counter`), `Root = (0,0)`, `Compare` (replica then counter), `String` (`r:c`) | 2 | ID order is part of the spec (section 1) | derive `Ord` with fields in that order; a unit test that `(1,9) < (2,1)` |
| 4 | `op.go` | `OpKind`, `Side`, `Op`, `Validate` (spec section 2), `Dependency`, `String` | 2, 3 | an op is valid only under section 2 | `validate_rejects_malformed_ops` (from `TestValidateRejectsMalformedOps`), minus the cases the enum makes unrepresentable |
| 5 | `version.go` | `VersionVector` (map replica to last counter), `Get`, `Contains`, `Clone`, `Replicas` (sorted), `Equal`, `Total` | 3 | `contains((r,c))` iff `c <= V[r]` | covered by `ops_since_returns_only_missing_ops` |
| 6 | `sequence.go`, the types only | `node` (id, value, deleted, parent, side, left/right children, prev/next) and `Sequence` (replica, lamport, root, node index, version vector, log, log position, visible count) | 3, 4, 5 | the structure I1–I7 describe | compiles with `Vec<Node>` and `NodeIdx(u32)` (section 5) |
| 7 | `invariants.go` | `CheckInvariants` (I1–I7) and the iterative in-order walk with `walkFrame` | 6 | I1–I7 | `check_invariants_walks_a_hundred_thousand_deep_chain_on_a_small_stack` and `check_invariants_names_the_broken_parent_and_child_order` pass; both corrupt or stress private state, so they are `#[cfg(test)]` unit tests inside the crate |
| 8 | `sequence.go`, the logic | `New`, `Len`, `Text`, `Insert` and `Delete` (local edits, positions in code points), `Apply` (spec section 6), `OpsSince`, `IDAt`, `IndexOf`, and the helpers `newInsert`, `applyInsert`, `applyDelete`, `record`, `checkDuplicate`, `visibleAt`, `insertSorted`, `firstInSubtree`, `lastInSubtree`, `linkAfter`, `linkBefore` | 6, 7 | every public method leaves I1–I7 true; a rejected `Apply` changes nothing | the `sequence_test.go` tests (`new_rejects_reserved_replica`, `empty_sequence_is_valid`, `local_insert_and_delete`, `indexes_count_code_points_not_bytes`, `local_edits_reject_out_of_range`, `local_edits_match_slice_model`, `apply_is_idempotent`, `apply_rejects_out_of_order_without_changes`, `apply_rejects_reused_id`, `cursor_anchors_survive_remote_edits`, `ops_since_returns_only_missing_ops`) and all seven fixtures in `testdata/conformance/crdt/` |
| 9 | `causal.go` | `CausalBuffer`: hold ops that are not ready, apply each as soon as it is, drop permanent failures, `DefaultMaxPending = 100_000` | 8 | never holds more than `max_pending`; an applied op is never held | `convergence_under_reordering_and_duplication`, `replaying_the_log_reproduces_the_text`, `concurrent_typing_at_same_position_does_not_interleave`, `concurrent_backward_typing_does_not_interleave` |
| 10 | `codec.go` | `EncodeOps`, `DecodeOps`, the minimal-uvarint decoder (spec section 7) | 4 | exactly one valid encoding per batch; the decoder never panics or allocates from an unchecked count | `codec_golden_bytes` (the bytes in spec section 7), `codec_encodes_empty_batch`, `decode_rejects_malformed_input`, `codec_round_trips_random_histories` |
| 11 | tests: `helpers_test.go`, `conformance_test.go` | seeded RNG with `SKEIN_SEED`, `must_*` helpers, `sync_all`, random edits; the fixture runner (spec section 8) | 8, 9 | none | the runner loads every file in `testdata/conformance/crdt/` and rejects unknown keys |
| 12 | fuzz: `FuzzDecodeOps`, `FuzzApply` in `codec_test.go` | decode arbitrary bytes; apply decoded ops to a sequence and check invariants | 7, 8, 10 | decoding never panics; any accepted op keeps I1–I7 | `cargo fuzz run decode_ops` and `apply` run clean for a few minutes each |
| 13 | benchmarks: `bench_test.go` | `AppendTyping`, `RandomEdits`, `MergeConcurrentHistories`, `EncodeDecode` | all | none | criterion numbers recorded beside the Go ones in `docs/PERF.md` |

Fixtures that prove the crate done (`testdata/conformance/crdt/`):
`001-concurrent-forward-typing`, `002-concurrent-insert-in-middle`,
`003-insert-next-to-concurrent-delete`, `004-concurrent-delete-same-character`,
`005-three-replicas-relayed`, `006-concurrent-backward-typing`,
`007-multibyte-text`. Each has a `why` field deriving the result from the
spec; read the `why` before debugging a failure. The golden bytes are in
spec section 7 and `TestCodecGoldenBytes`.

## 4. Dependencies

Imports: Go standard library only (`encoding/binary` for uvarints, `errors`,
`fmt`, `slices`, `unicode/utf8`, `math/rand/v2` in tests).

| Need | Rust | Note |
|---|---|---|
| errors | `thiserror` | one enum, section 5 |
| uvarint | write it (about 20 lines) | must reject non-minimal encodings and values over 64 bits. General varint crates (for example `integer-encoding`) are written for protobuf and do not promise to reject overlong forms, so do not rely on them for decoding |
| node index | `std::collections::HashMap<Id, NodeIdx>` | order never observed |
| version vector | `BTreeMap<ReplicaId, u64>` | `Replicas()` is sorted in Go; `BTreeMap` makes that implicit |
| property tests | `proptest` (dev) | for the slice-model and convergence tests |
| seeded RNG | `rand` + `rand_pcg` (dev) | seeds are not shared with Go; fixtures are |
| fuzzing | `cargo-fuzz` (`libfuzzer-sys`) | separate `fuzz/` crate |
| benchmarks | `criterion` (dev) | |

Write the CRDT yourself: that is the point of the exercise, and no existing
crate implements this exact tree, tie-break and wire format.

What depends on it: `skein-blocks`, the documents service crate, the WASM
bridge crate, and the seed tool if it is ported.

## 5. Rust layout

Crate `skein-crdt`, edition 2024, `#![forbid(unsafe_code)]`, `no_std` not
needed. Keep it free of `wasm-bindgen`; the bridge crate wraps it.

```
skein-crdt/
  src/
    lib.rs          //! docs from doc.go; pub use of the items below
    error.rs        errors.go
    id.rs           id.go
    op.rs           op.go
    version.rs      version.go
    sequence.rs     sequence.go (Node arena, local edits, apply)
    invariants.rs   invariants.go (check_invariants, iterative walk)
    causal.rs       causal.go
    codec.rs        codec.go (+ the uvarint reader and writer)
  tests/
    common/mod.rs   helpers_test.go
    conformance.rs  conformance_test.go
    convergence.rs  convergence_test.go
    codec.rs        codec_test.go (golden, malformed, round trip)
    sequence.rs     sequence_test.go
  fuzz/fuzz_targets/{decode_ops.rs, apply.rs}
  benches/sequence.rs
```

Public API sketch:

```rust
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord, Default)]
pub struct ReplicaId(pub u64);

/// Ordered by replica, then counter (spec section 1): derive keeps field order.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord, Default)]
pub struct Id { pub replica: ReplicaId, pub counter: u64 }

impl Id {
    pub const ROOT: Id = Id { replica: ReplicaId(0), counter: 0 };
    pub fn is_root(self) -> bool;
}
impl std::fmt::Display for Id { /* "replica:counter" */ }

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Side { Left = 0, Right = 1 }

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Op { pub id: Id, pub lamport: u64, pub body: OpBody }

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum OpBody {
    Insert { parent: Id, side: Side, value: char },
    Delete { target: Id },
}

impl Op {
    pub fn validate(&self) -> Result<(), Error>;
    pub fn dependency(&self) -> Id;
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct VersionVector(BTreeMap<ReplicaId, u64>);
impl VersionVector {
    pub fn get(&self, r: ReplicaId) -> u64;
    pub fn contains(&self, id: Id) -> bool;
    pub fn replicas(&self) -> impl Iterator<Item = ReplicaId> + '_;
    pub fn total(&self) -> u64;
}

pub struct Sequence { /* nodes: Vec<Node>, index: HashMap<Id, NodeIdx>, vv, log, log_pos, visible, lamport, replica */ }

impl Sequence {
    pub fn new(replica: ReplicaId) -> Result<Self, Error>;
    pub fn replica(&self) -> ReplicaId;
    pub fn len(&self) -> usize;              // visible code points
    pub fn lamport(&self) -> u64;
    pub fn version(&self) -> VersionVector;
    pub fn has(&self, id: Id) -> bool;
    pub fn text(&self) -> String;
    pub fn insert(&mut self, index: usize, text: &str) -> Result<Vec<Op>, Error>;
    pub fn delete(&mut self, index: usize, count: usize) -> Result<Vec<Op>, Error>;
    pub fn apply(&mut self, op: &Op) -> Result<(), Error>;
    pub fn ops_since(&self, v: &VersionVector) -> Vec<Op>;
    pub fn id_at(&self, index: usize) -> Result<Id, Error>;
    /// (index, visible) or None when the id is unknown; Go returns (index, visible, ok).
    pub fn index_of(&self, id: Id) -> Option<(usize, bool)>;
    pub fn check_invariants(&self) -> Result<(), Error>;
}

pub const DEFAULT_MAX_PENDING: usize = 100_000;
pub struct CausalBuffer<'a> { seq: &'a mut Sequence, pending: Vec<Op>, max_pending: usize }
impl<'a> CausalBuffer<'a> {
    pub fn new(seq: &'a mut Sequence, max_pending: usize) -> Self; // 0 selects the default
    pub fn pending(&self) -> usize;
    pub fn deliver(&mut self, ops: impl IntoIterator<Item = Op>) -> Result<(), Error>;
}

pub fn encode_ops(ops: &[Op]) -> Vec<u8>;
pub fn decode_ops(data: &[u8]) -> Result<Vec<Op>, Error>;
```

A borrowed `CausalBuffer<'a>` is the direct port, but the documents actor and
the bridge both keep the buffer and the sequence together for a note's
lifetime; an owning `CausalBuffer { seq: Sequence, .. }` with `seq()` and
`seq_mut()` avoids fighting the borrow checker there. Pick one and keep it.

Error enum:

```rust
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum Error {
    #[error("crdt: invalid operation: {0}")]            InvalidOp(String),
    #[error("crdt: operation is not causally ready: {0}")] NotReady(Id),
    #[error("crdt: operation id reused with different content: {0}")] IdConflict(Id),
    #[error("crdt: index out of range: {index} (len {len})")] IndexOutOfRange { index: usize, len: usize },
    #[error("crdt: replica id 0 is reserved")]          InvalidReplica,
    #[error("crdt: too many pending operations")]       BufferFull,
    #[error("crdt: corrupt encoding: {0}")]             Corrupt(String),
    #[error("crdt: invariant violated: {0}")]           Invariant(String),
}
```

`ErrInvalidText` disappears: a Rust `&str` is always valid UTF-8. Keep it
only if the bridge accepts raw bytes.

Feature flags: none needed. If you add the optimised representation later,
put the reference one behind `#[cfg(test)]` or a `reference` feature and
run both operation for operation (ADR-0005, last bullet).

WASM: the bridge is a separate crate (`skein-wasm`, the port of
`web/wasm/bridge` and `web/wasm/cmd/skeincrdt`) over `skein-crdt` and
`skein-blocks`. Its surface is described in
[`libs/blocks/PORTING.md`](../blocks/PORTING.md#5-rust-layout), because the
bridge's document type is `blocks.Document`.

## 6. Pitfalls and cross-checking

**Positions.** Every index counts code points. Do not slice `&str` at a
code point index; collect `chars()` or use `char_indices`. The browser
converts UTF-16 to code points in TypeScript (`web/src/crdt/utf16.ts`), so
the Rust bridge receives code points, as the Go one does. Two Rust-specific
traps at that boundary: `wasm-bindgen` passes `&str` by copying through
`TextEncoder`, and it [replaces unpaired surrogates with U+FFFD](https://wasm-bindgen.github.io/wasm-bindgen/reference/types/str.html),
so a lone surrogate typed in the browser becomes one U+FFFD code point
(the same count the TypeScript `TextIndex` gives it, so offsets still
agree). Take `js_sys::JsString` only if you ever need the raw units.

**Recursion depth.** Typing forward makes each character the right child of
the one before, so a 100,000-character note is a 100,000-deep chain. The
Go walk in `invariants.go` is iterative (`walkFrame`), and
`TestCheckInvariantsWalksAHundredThousandDeepChainOnASmallStack` caps the Go
stack at 1 MiB to prove it. Rust test threads have 2 MiB and WASM stacks
are small: keep `in_order`, `first_in_subtree` and `last_in_subtree` as
loops, never recursion.

**Sibling order.** Children are sorted ascending by `(replica, counter)`. A
derive of `Ord` on `Id` with fields in that order gives exactly this; do
not sort by Lamport or by insertion order.

**Integer widths.** Counters, replicas and Lamport timestamps are `u64` in
memory and in the wire format. Lengths and indexes are `usize` and never
appear in a format. `char` covers spec section 2's value rule (scalar
values only); decode the wire `value` as `u64`, then `char::from_u32` after
checking it fits `u32`, and reject surrogates.

**Overflow.** Go wraps silently. The Lamport clock and counters are
incremented: use `checked_add` and return `Error::InvalidOp` (or treat
`u64::MAX` as unreachable) rather than panicking in debug builds on hostile
input.

**Map order.** The node index and `log_pos` are lookups only. Anything
encoded or compared is sorted in Go first; `BTreeMap` for the version
vector removes the need.

**Not ready is not an error to log.** `Apply` returning `NotReady` is the
normal path for out-of-order delivery and changes nothing; the buffer
retries it. Only `InvalidOp`, `IdConflict` and `BufferFull` are permanent.

**Codec.** The decoder must reject: wrong magic, unknown version or kind,
`count > remaining / 6` (before allocating), truncation, trailing bytes,
non-minimal varints, and any op invalid under spec section 2. A 10-byte
uvarint whose last byte carries more than one bit overflows `u64`; reject
it.

Cross-checking against Go:

1. **Fixtures.** `tests/conformance.rs` runs `testdata/conformance/crdt/*.json`
   with the semantics of spec section 8 (`sync "all"` visits receivers in
   ascending name order). Unknown keys are an error.
2. **Golden bytes.** `codec_golden_bytes` asserts the exact bytes of spec
   section 7 (`53 4B 01 02`, then `01 01 01 01 00 00 01 68`, then
   `02 AC 02 02 05 01 01`).
3. **Cross-language replay.** `docs/porting-to-rust.md` section 6 plans
   `make crosscheck`: Go writes random encoded histories to
   `testdata/crosscheck/`, Rust decodes, replays and compares the final
   text and version vector, then the other way round. The seed histories
   in `testdata/seed/small` are a second corpus that already exists: decode
   each note's ops in Rust and compare the text with the seed's ground
   truth (see `docs/seed-data.md`).
4. **Differential fuzzing.** Feed the same fuzz input to Go
   (`FuzzDecodeOps`) and Rust and compare accept/reject plus the
   re-encoded bytes. The simplest harness: a small Go program that reads
   files from the Rust fuzz corpus and prints `ok <hex>` or `err`, run over
   `fuzz/corpus/decode_ops` after a fuzz session.
5. **Browser.** `web/src/crdt/conformance.test.ts` runs the same fixtures
   through the WASM module and the UTF-16 wrapper. When the Rust WASM build
   replaces the Go one, that Vitest suite passing unchanged is the
   end-to-end proof.
