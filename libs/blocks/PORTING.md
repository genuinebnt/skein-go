# Porting `libs/blocks` to Rust (`skein-blocks`)

The block model on top of the sequence CRDT: how one note's text sequence
is divided into blocks, the four last-writer-wins attributes of a block,
the inline Markdown inside a block, and Markdown import and export of whole
notes. With `skein-crdt` it is everything the browser's WASM module does.

Normative spec: [`docs/spec/blocks.md`](../../docs/spec/blocks.md) (builds on
[`crdt-sequence.md`](../../docs/spec/crdt-sequence.md)).
Decisions: [ADR-0005](../../docs/adr/0005-sequence-crdt.md) (one sequence per
note with block markers), [ADR-0003](../../docs/adr/0003-text-positions.md),
[ADR-0007](../../docs/adr/0007-offline-wasm-client.md).
General rules: [`docs/porting-to-rust.md`](../../docs/porting-to-rust.md)
(section 5 has blocks-specific notes). Port `libs/crdt` first
([`libs/crdt/PORTING.md`](../crdt/PORTING.md)).

## 1. What it is and why Skein needs it

Notes are Notion-style blocks with Markdown inside each block. ADR-0005
keeps one Fugue sequence per note and starts each block with a marker
element (U+001E), so Enter and Backspace are plain inserts and deletes and
concurrent typing always survives a split or merge. Block type, indent,
checked state and code language are LWW registers kept beside the sequence
(`Attributes`), ordered by `(lamport, replica)`. Inline formatting is
Markdown text read by a tolerant parser that never fails.

| Consumer | What it uses | Feature |
|---|---|---|
| `services/documents` (`internal/app/service.go`, `state.go`, `history.go`; `internal/domain/batch.go`, `events.go`, `history.go`, `undo.go`; `internal/adapters/postgres/store.go`; `internal/seed/loader.go`) | `Document`, `Import`, `Export`, `ExportMapped`, `Elements`, `Layout`, `Attributes.At`, `EncodeAttrs`/`DecodeAttrs`, `Links` | `CreateNote` (import Markdown), `GetNote` (export), `GetTextAt` and `Diff` (layout at a version), `Blame` (`ExportMapped` source map), `Undo`/`Redo`; `BlocksChanged` and `LinksChanged` events (`events.go`, consumed by search for the trigram index and the link graph); attribute batches in snapshots |
| `web/wasm/bridge` and `web/wasm/cmd/skeincrdt` | `Document`, `ParseInline`, `Export`, `ExportMapped`, `Import`, attribute codec | the editor: render blocks and inline nodes, set attributes, history, attribution, replace with Markdown |
| `services/notifications` (through events) | `Mentions` | mention notifications |

## 2. Learn first

| # | Source | What to take from it | Maps to | Time |
|---|---|---|---|---|
| 1 | [Automerge rich text schema](https://automerge.org/docs/reference/under-the-hood/rich-text-schema/) | Block markers inside the character sequence, text after a marker belonging to that block: the same design as ADR-0005 | `elements.go` (`Layout`), spec section 1 | 0.5 h |
| 2 | Litt, Lim, Kleppmann, van Hardenberg, ["Peritext: A CRDT for Rich-Text Collaboration"](https://inkandswitch.com/peritext/) (Ink & Switch 2021; CSCW 2022) | Why formatting as marks anchored to characters is hard to merge, and what Skein gives up by keeping inline formatting as Markdown text (ADR-0005 consequences) | `inline.go`, ADR-0005 | 1.5 h |
| 3 | Shapiro et al., [comprehensive study of CRDTs](https://webarchive.di.uminho.pt/haslab.uminho.pt/cbm/publications/comprehensive-study-convergent-and-commutative-replicated-data-types.html), the LWW-register section | Last-writer-wins registers with a total order on stamps; Skein adds the encoded value as the final tie-break | `attrs.go` (`Wins`, `Apply`) | 0.5 h |
| 4 | [CommonMark spec 0.31.2](https://spec.commonmark.org/spec), section 6.2 "Emphasis and strong emphasis" and the appendix "A parsing strategy" | Delimiter runs, left- and right-flanking, the `_` rules, and the "process emphasis" algorithm. Skein follows it without the rule of three (rules 9 and 10) | `inline.go` (`flanking`, `matchEmphasis`) | 2 h |
| 5 | [pulldown-cmark](https://github.com/pulldown-cmark/pulldown-cmark) (Rust) | A Rust CommonMark parser over `&str` with source offsets; read its inline delimiter handling for idioms (byte offsets, borrowed text). Do not depend on it: Skein's grammar is a small tolerant subset with code point spans | `inline.go`, `markdown.go` | browse |
| 6 | Notion, ["The data model behind Notion's flexibility"](https://www.notion.com/blog/data-model-behind-notion) | The block model Skein imitates (type, properties, content), and why Skein flattens nesting into an indent attribute instead | `block.go`, ADR-0005 | 0.5 h |
| 7 | [`unicode-general-category`](https://lib.rs/crates/unicode-general-category) | The Rust way to ask Go's `unicode.IsPunct`/`IsSymbol`/`IsLetter`/`IsMark` questions by general category | `inline.go` (`isPunct`, `isHandleStart`, `flanking`) | 0.25 h |
| 8 | [wasm-bindgen: `str` and `String`](https://wasm-bindgen.github.io/wasm-bindgen/reference/types/str.html) | How strings cross the JS boundary (copied, unpaired surrogates become U+FFFD) | the bridge (section 5) | 0.25 h |

Reading order: 1, 3, 4 (the parts the code needs), then 2 and 6 for
context, 5, 7 and 8 while porting. About 6 hours.

## 3. Port order

| # | Go file | Contents | Needs | Invariants and rules | Done when |
|---|---|---|---|---|---|
| 1 | `doc.go` | package docs | none | | `lib.rs` `//!` docs |
| 2 | `errors.go` | `ErrInvalidAttr`, `ErrCorrupt`, `ErrTooManyElements`, `ErrMarkerInText`, `ErrInvariant` | none | | the enum in section 5 |
| 3 | `block.go` | `Marker = U+001E`, `Type` (1–11) with names, `Valid`, `IsList`, `ParseType`, `MaxIndent = 128`, `MaxLanguageBytes = 64`, `Attrs`, `DefaultAttrs`, `Block` (id, attrs, text, per-character IDs), `Content` | crdt | type codes are the wire values (spec 3) | compiles; covered by later tests |
| 4 | `attrs.go`, types and checker | `AttrKey` (1–4), `AttrWrite`, `Validate`, `validateLanguage`, `Wins`, `register`, `Attributes` struct, `CheckInvariants` (A1–A3) | 3 | A1 writes valid and distinct, A2 each winner is the maximum, A3 clock is the largest stamp | `validate_rejects_malformed_writes` |
| 5 | `attrs.go`, logic | `NewAttributes`, `Apply`, `Get`, `Lamport`, `Len`, `Writes`, `At`, `Diff` | 4 | winner order `(lamport, replica)` then encoded value bytes | `later_lamport_wins_whatever_the_arrival_order`, `equal_lamport_is_broken_by_replica_then_value`, `reapplying_a_write_changes_nothing`, `registers_of_one_block_are_independent`, `unwritten_block_has_default_attributes`, `at_keeps_only_writes_up_to_the_timestamp`, `diff_writes_only_changed_registers`, `attributes_converge_under_any_order_and_duplication` |
| 6 | `codec.go` | `EncodeAttrs`, `DecodeAttrs` (spec section 4) | 4, 5 | one valid encoding; same uvarint rules as crdt | `codec_golden_bytes` (spec 4's four writes), `decode_rejects_malformed_bytes`; fixture 012 (`encoded_hex`) |
| 7 | `elements.go` | `Element`, `MaxElements`, `Elements` (relabelled replay), `label`/`unlabel`, `Layout`, `Compose`, `Document` (`NewDocument`, `Sequence`, `Attributes`, `Lamport`, `Elements`, `Blocks`, `AppendBlocks`, `SetAttrs`, `CheckInvariants` L1–L3) | crdt, 3, 5 | L1–L3; relabel skips the surrogate range | `elements_match_the_sequence_order_under_concurrent_editing`, `enter_splits_a_block_and_backspace_merges_it`, `text_before_the_first_marker_forms_a_leading_block`, `append_blocks_stamps_attributes_with_the_sequence_clock`, `set_attrs_beats_every_write_seen`, `append_blocks_rejects_marker_in_text`, `compose_is_the_inverse_of_layout`, `elements_rejects_an_insert_before_its_parent`, `elements_order_past_the_surrogate_range`; fixtures 013, 014 |
| 8 | `inline.go`, types and checker | `NodeKind` (1–8), `Node` (kind, span, content span, value, children), `CheckTree` (P1–P3), `checkDelimiters`, `PlainText` | none | P1 cover, P2 nesting and no adjacent text, P3 delimiters | used by every inline test |
| 9 | `inline.go`, parser | `ParseInline`, `parseRange` (tokenising rules 1–7), `delim`, `matchEmphasis`, `treeBuilder`, `backtickRuns`, `wikiLink`, `link`, `mention`, `flanking`, `isPunct`, `isASCIIPunct`, `isHandleStart` | 8 | spec section 5, linear time | `parse_inline_examples`, `spans_count_code_points`, `parse_inline_invariants_hold_on_random_text`, `wiki_links_match_the_link_pattern_outside_code`, `long_delimiter_runs_parse_in_linear_time`; fixtures 001–007 |
| 10 | `extract.go` | `Links`, `Mentions` (distinct, sorted, skipping code blocks) | 3, 9 | byte order sort | `links_and_mentions_skip_code`; fixtures 001–007 (`links`, `mentions`) |
| 11 | `markdown.go` | `Source`, `Export`, `ExportMapped`, `mdWriter`, `codeFenceLength`, `classify`, `needsEscape`, list numbering, `Import`, `paragraphText`, `CheckLossless`, `tableText` | 3 | lossless subset round trip; export of import is a fixed point; source map | `import_reads_each_block_type`, `export_numbers_lists_per_indent`, `export_escapes_paragraphs_that_look_like_other_blocks`, `code_fence_outgrows_backtick_lines_in_the_code`, `import_tolerates_unclosed_fence_and_hostile_input`, `import_of_export_gives_back_lossless_blocks`, `export_of_import_is_a_fixed_point`, `export_mapped_points_every_text_code_point_at_its_source`, `seed_notes_round_trip_through_markdown` (reads `testdata/seed/small`); fixtures 008–011 |
| 12 | tests: `helpers_test.go`, `conformance_test.go` | seeded RNG, `new_doc`, `must_valid`, the fixture runner by `kind` | all | | every file in `testdata/conformance/blocks/` |
| 13 | fuzz: `FuzzDecodeAttrs`, `FuzzParseInline`, `FuzzImport` | decoder never panics; parse output passes `CheckTree`; import then export is a fixed point | 6, 9, 11 | | `cargo fuzz` targets run clean; include the Go corpus entry `libs/blocks/testdata/fuzz/FuzzImport/e6a5d81a40b57795` (the input `"0\r"`) and the `f.Add` seeds |
| 14 | benchmarks: `bench_test.go` | `Elements10k`, `ParseInlineParagraph`, `ExportImport100Blocks` | all | | criterion numbers beside Go's in `docs/PERF.md` |

Fixtures (`testdata/conformance/blocks/`): inline parsing 001–007, Markdown
import 008–009, export 010–011, attribute registers with golden bytes 012,
layout 013–014.

## 4. Dependencies

Imports: `libs/crdt` and the Go standard library (`unicode`,
`unicode/utf8`, `strings`, `slices`, `encoding/binary`, `strconv`, `fmt`,
`errors`).

| Need | Rust | Note |
|---|---|---|
| the sequence | `skein-crdt` | path dependency in the workspace |
| errors | `thiserror` | |
| Unicode categories P, S, L, N, M | `unicode-general-category` | Go 1.24 uses Unicode 15.0 (section 6) |
| uvarint | share the codec helpers with `skein-crdt` (make them `pub` in a `varint` module there, or a tiny `skein-varint` crate) | minimal encodings only |
| Markdown parser | write it | the grammar is Skein's own subset with code point spans and a lossless tree; no crate gives both |
| attribute history (`seen`, winners) | `HashSet<AttrWrite>`, `HashMap<(Id, AttrKey), AttrWrite>` | lookups only; `writes: Vec<AttrWrite>` keeps arrival order |
| property tests, fuzzing, benchmarks | `proptest`, `cargo-fuzz`, `criterion` | |

What depends on it: the documents service crate and the WASM bridge crate.

## 5. Rust layout

Crate `skein-blocks`, edition 2024, `#![forbid(unsafe_code)]`, depending on
`skein-crdt`. No `wasm-bindgen` here.

```
skein-blocks/
  src/
    lib.rs        //! from doc.go
    error.rs
    block.rs      Marker, BlockType, Attrs, Block
    attrs.rs      AttrKey, AttrValue, AttrWrite, Attributes
    codec.rs      attribute batches
    elements.rs   Element, elements(), layout(), compose(), Document
    inline.rs     Node, parse_inline, check_tree, plain_text
    extract.rs    links, mentions
    markdown.rs   export, export_mapped, import, check_lossless
  tests/{conformance.rs, attrs.rs, elements.rs, inline.rs, markdown.rs}
  fuzz/fuzz_targets/{decode_attrs.rs, parse_inline.rs, import.rs}
  benches/blocks.rs
```

Public API sketch:

```rust
pub const MARKER: char = '\u{1E}';
pub const MAX_INDENT: u32 = 128;
pub const MAX_LANGUAGE_BYTES: usize = 64;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
#[repr(u8)]
pub enum BlockType { Paragraph = 1, Heading1, Heading2, Heading3, Bulleted, Numbered, Todo, Quote, Code, Divider, Table }
impl BlockType { pub fn is_list(self) -> bool; pub fn parse(name: &str) -> Option<Self>; pub fn name(self) -> &'static str; }

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Attrs { pub block_type: BlockType, pub indent: u32, pub checked: bool, pub language: String }
impl Default for Attrs { /* Paragraph, 0, false, "" */ }

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Block { pub id: Id, pub attrs: Attrs, pub text: String, pub chars: Vec<Id> }

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum AttrKey { Type = 1, Indent = 2, Checked = 3, Language = 4 }

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum AttrValue { Type(BlockType), Indent(u32), Checked(bool), Language(String) }

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct AttrWrite { pub block: Id, pub lamport: u64, pub replica: ReplicaId, pub value: AttrValue }
impl AttrWrite {
    pub fn key(&self) -> AttrKey;
    pub fn validate(&self) -> Result<(), Error>;
    pub fn wins(&self, other: &AttrWrite) -> bool;
}

pub struct Attributes { /* writes: Vec<AttrWrite>, seen: HashSet<AttrWrite>, winners: HashMap<(Id, AttrKey), AttrWrite>, lamport: u64 */ }
impl Attributes {
    pub fn new() -> Self;
    pub fn apply(&mut self, w: AttrWrite) -> Result<bool, Error>; // changed
    pub fn get(&self, block: Id) -> Attrs;
    pub fn lamport(&self) -> u64;
    pub fn writes(&self) -> &[AttrWrite];
    pub fn at(&self, max_lamport: u64) -> Attributes;
    pub fn diff(&self, block: Id, want: &Attrs, lamport: u64, replica: ReplicaId) -> Vec<AttrWrite>;
    pub fn check_invariants(&self) -> Result<(), Error>;
}
pub fn encode_attrs(writes: &[AttrWrite]) -> Vec<u8>;
pub fn decode_attrs(data: &[u8]) -> Result<Vec<AttrWrite>, Error>;

pub struct Element { pub id: Id, pub value: char, pub deleted: bool }
pub fn elements(ops: &[Op]) -> Result<Vec<Element>, Error>;
pub fn layout(elems: &[Element], attrs: &Attributes) -> Vec<Block>;
pub fn compose(blocks: &[Block]) -> Result<String, Error>;

pub struct Document { seq: Sequence, attrs: Attributes }
impl Document {
    pub fn new(replica: ReplicaId) -> Result<Self, Error>;
    pub fn sequence(&self) -> &Sequence;
    pub fn sequence_mut(&mut self) -> &mut Sequence;
    pub fn attributes(&self) -> &Attributes;
    pub fn lamport(&self) -> u64;
    pub fn elements(&self) -> Result<Vec<Element>, Error>;
    pub fn blocks(&self) -> Result<Vec<Block>, Error>;
    pub fn append_blocks(&mut self, blocks: &[Block]) -> Result<(Vec<Op>, Vec<AttrWrite>), Error>;
    pub fn set_attrs(&mut self, block: Id, want: &Attrs) -> Result<Vec<AttrWrite>, Error>;
    pub fn check_invariants(&self) -> Result<(), Error>;
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NodeKind { Text = 1, Escape, Code, Emphasis, Strong, Link, WikiLink, Mention }
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Node {
    pub kind: NodeKind, pub start: usize, pub end: usize,
    pub content_start: usize, pub content_end: usize,
    pub value: String, pub children: Vec<Node>,
}
pub fn parse_inline(text: &str) -> Vec<Node>;
pub fn check_tree(text: &str, nodes: &[Node]) -> Result<(), Error>;
pub fn plain_text(nodes: &[Node]) -> String;
pub fn links(blocks: &[Block]) -> Vec<String>;
pub fn mentions(blocks: &[Block]) -> Vec<String>;

pub struct Source { pub block: usize, pub offset: usize }
pub fn export(blocks: &[Block]) -> String;
pub fn export_mapped(blocks: &[Block]) -> (String, Vec<Source>);
pub fn import(markdown: &str) -> Vec<Block>;
pub fn check_lossless(blocks: &[Block]) -> Result<(), Error>;
```

`AttrWrite` becomes a struct with an `AttrValue` enum
(`docs/porting-to-rust.md` section 5), which deletes `Validate`'s checks
that "the fields of another key are zero". `Node` can stay one struct with
a `kind` (the porting guide suggests an enum per kind; a struct keeps
`CheckTree` and the fixture runner simpler, and spans are common to all
kinds). Pick one and keep it.

Error enum:

```rust
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum Error {
    #[error("blocks: invalid attribute write: {0}")]      InvalidAttr(String),
    #[error("blocks: corrupt encoding: {0}")]             Corrupt(String),
    #[error("blocks: too many elements to order: {0}")]   TooManyElements(usize),
    #[error("blocks: block text contains the block marker")] MarkerInText,
    #[error("blocks: invariant violated: {0}")]           Invariant(String),
    #[error(transparent)]                                  Crdt(#[from] skein_crdt::Error),
}
```

Feature flags: none in the library.

### The WASM bridge (`skein-wasm`)

The port of `web/wasm/bridge` (the logic) and `web/wasm/cmd/skeincrdt`
(the JS glue) is its own crate, depending on `skein-crdt`, `skein-blocks`,
`wasm-bindgen`, `js-sys`, `serde-wasm-bindgen` and `slab`. Port
`web/wasm/bridge/document_test.go` with it, as native Rust tests of the
bridge logic (they need no browser). The TypeScript wrapper
(`web/src/crdt/wasm.ts`) expects `globalThis.skeinCrdt` with exactly the
functions of its `RawModule` type, handle-based, returning
`{ error: string }` instead of throwing, and a `skeincrdtready` event. Keep
that contract so the wrapper and its tests do not change (ADR-0007):

```rust
// One document per handle; WASM is single-threaded, so a thread_local slab is enough.
thread_local! { static DOCS: RefCell<Slab<Note>> = RefCell::new(Slab::new()); }
struct Note { doc: skein_blocks::Document, pending: Vec<Op> /* or an owning CausalBuffer */ }

#[wasm_bindgen(js_name = newDocument)]   pub fn new_document(replica: &str) -> Result<u32, JsError>; // u64 as a decimal string
#[wasm_bindgen]                          pub fn free(handle: u32);
#[wasm_bindgen(js_name = applyRemote)]   pub fn apply_remote(handle: u32, ops: &[u8]) -> Result<JsValue, JsError>; // { pending }
#[wasm_bindgen(js_name = applyRemoteAttrs)] pub fn apply_remote_attrs(handle: u32, attrs: &[u8]) -> Result<(), JsError>;
#[wasm_bindgen]                          pub fn insert(handle: u32, pos: u32, text: &str) -> Result<Vec<u8>, JsError>;
#[wasm_bindgen(js_name = delete)]        pub fn delete_text(handle: u32, pos: u32, count: u32) -> Result<Vec<u8>, JsError>;
#[wasm_bindgen(js_name = setAttrs)]      pub fn set_attrs(handle: u32, block_id: &str, attrs: JsValue) -> Result<Vec<u8>, JsError>;
#[wasm_bindgen(js_name = seedAttrsFromMarkdown)] pub fn seed_attrs(handle: u32, markdown: &str) -> Result<u32, JsError>;
#[wasm_bindgen]                          pub fn text(handle: u32) -> Result<String, JsError>;
#[wasm_bindgen]                          pub fn blocks(handle: u32) -> Result<JsValue, JsError>;    // Block[]
#[wasm_bindgen]                          pub fn version(handle: u32) -> Result<JsValue, JsError>;   // VersionEntry[]
#[wasm_bindgen]                          pub fn lamport(handle: u32) -> Result<String, JsError>;    // u64 as a string
#[wasm_bindgen(js_name = opsSince)]      pub fn ops_since(handle: u32, version: JsValue) -> Result<Vec<u8>, JsError>;
#[wasm_bindgen(js_name = allAttrs)]      pub fn all_attrs(handle: u32) -> Result<Vec<u8>, JsError>;
#[wasm_bindgen(js_name = idAt)]          pub fn id_at(handle: u32, pos: u32) -> Result<String, JsError>;
#[wasm_bindgen(js_name = indexOf)]       pub fn index_of(handle: u32, id: &str) -> Result<JsValue, JsError>; // { index, visible, ok }
#[wasm_bindgen]                          pub fn markdown(handle: u32) -> Result<String, JsError>;
#[wasm_bindgen(js_name = markdownAt)]    pub fn markdown_at(handle: u32, version: JsValue) -> Result<String, JsError>;
#[wasm_bindgen(js_name = exportMapped)]  pub fn export_mapped(handle: u32) -> Result<JsValue, JsError>; // { markdown, ids }
#[wasm_bindgen(js_name = renderInline)]  pub fn render_inline(text: &str) -> JsValue;                // RawNode[]
#[wasm_bindgen(js_name = describeOps)]   pub fn describe_ops(ops: &[u8]) -> Result<JsValue, JsError>;
#[wasm_bindgen(js_name = checkInvariants)] pub fn check_invariants(handle: u32) -> Result<(), JsError>;
#[wasm_bindgen(js_name = replaceWithMarkdown)] pub fn replace_with_markdown(handle: u32, markdown: &str) -> Result<JsValue, JsError>; // { ops, attrs }
```

A short JS shim (the job `main.go` does in Go) imports the wasm-bindgen
module, wraps each export so a thrown error becomes `{ error: message }`,
assigns the object to `globalThis.skeinCrdt` and dispatches
`skeincrdtready`. Only `loadCrdt`'s `ModuleSource` in `wasm.ts` then needs
to know which build it is loading. Keep `BaselineReplica` and the
`SeedAttrsFromMarkdown` stamping exactly as `bridge/document.go` documents.

## 6. Pitfalls and cross-checking

**Code points, not bytes.** Parse over `Vec<char>` (or char indices); every
span, `Source.offset` and fixture position counts code points. The
browser converts UTF-16 at the TypeScript boundary, so the bridge sees
code points too.

**Recursion depth in the inline tree.** `"*"` repeated k times, a letter,
then k more `"*"` matches as k/2 nested strong nodes, and Go's
`treeBuilder.build` recurses once per level (Go grows its stack, so the Go
tests never notice). In Rust that overflows the stack for large k, and so
does the compiler-generated `Drop` of a deeply nested `Vec<Node>`, as do a
recursive `check_tree`, `plain_text` or the bridge's conversion to JS.
Build the tree with an explicit stack of open containers, write
`check_tree` and `plain_text` iteratively, implement `Drop` for `Node`
iteratively (or keep nodes in an arena), and add a test with k = 200,000
on the default 2 MiB test thread. Keep the output identical to Go's.

**Unicode tables.** Flanking uses Unicode punctuation and symbols
(categories P and S), the mention rule uses letters, digits and marks, and
Go 1.24's tables are Unicode 15.0. Use general categories, not
`char::is_alphabetic` (the Alphabetic property is a different set). Pin or
check the Unicode version of the crate you use.

**Invalid UTF-8.** `Import` replaces invalid UTF-8 with
`strings.ToValidUTF8`, which writes **one U+FFFD per run** of invalid
bytes. That differs from Go's per-byte `range` decoding (used by `libs/dp`
and `libs/trigram`) and from Rust's `String::from_utf8_lossy` (one per
maximal invalid subsequence). A Rust `import(&str)` never sees invalid
bytes, and the services only pass protobuf strings, so take `&str` and
leave the invalid-byte cases of `import_tolerates_unclosed_fence_and_hostile_input`
out with a comment. U+001E in imported Markdown still becomes U+FFFD.

**Element order without an iterator.** `Elements` replays the log's
inserts into a scratch sequence, renumbering each replica's inserts densely
and labelling the k-th insert with code point `k` (or `k + 0x800` from
`0xD800`, skipping surrogates). In Rust, `char::from_u32` on a surrogate
returns `None`, so a wrong skip fails loudly rather than silently; still
port `elements_order_past_the_surrogate_range`. Better: add an element
iterator to `skein-crdt` (`Sequence::elements()`, walking the linked list),
use it here, and keep the relabelled replay as a test oracle, as
`docs/porting-to-rust.md` section 5 suggests.

**Attribute ties.** `Wins` compares `(lamport, replica)` and then the
**encoded value bytes** (spec 3). Encode the value with the same function
the codec uses and compare `&[u8]`; deriving `Ord` on `AttrValue` gives a
different order.

**Stamping.** A new write is stamped one above the largest Lamport
timestamp seen in operations *and* writes (`Document::lamport` is the max
of both). Markdown import may stamp with the sequence clock after the
inserts (spec 3, "Stamping").

**Integer widths.** Indent is `u32` capped at 128 and a uvarint on the
wire; Lamport and replica are `u64`. In the bridge, replicas and Lamport
timestamps cross to JS as decimal strings because they can exceed 2^53;
`wasm-bindgen` maps `u64` to `BigInt`, which would change the TypeScript
types, so keep strings.

**Map order.** `Links` and `Mentions` sort their output by UTF-8 bytes;
`Attributes` keeps arrival order in `writes` and uses maps only for
lookups. Nothing else is iterated.

**Complexity.** `Document::check_invariants` is O(n²) by design (it asks
the sequence for every element's position). Fine in tests; never call it
in the documents service's hot path.

Cross-checking against Go:

1. **Fixtures.** `tests/conformance.rs` runs `testdata/conformance/blocks/*.json`
   by `kind` (spec section 7). The `attrs` kind applies writes in order
   and in reverse and checks `encoded_hex`; the `layout` kind replays
   edits and attribute writes on one replica.
2. **Golden bytes.** Spec section 4's four writes encode to
   `53 41 01 04 | 01 01 01 02 01 03 | 04 AC02 05 07 AC02 02 676F | 03 00 00 03 02 01 | 02 01 09 04 01 8001`.
3. **Seed round trip.** Port `seed_notes_round_trip_through_markdown`: every
   note in `testdata/seed/small` exports and imports back to the same
   blocks, and matches the Markdown the Go service would return.
4. **Cross-language documents.** Decode a seed note's op log and attribute
   batch (Go-encoded) in Rust, lay it out and export it, and compare with
   Go's `Export` output for the same note (dump those once into
   `testdata/crosscheck/blocks/`).
5. **Differential fuzzing.** Run the `parse_inline` and `import` fuzz
   corpora through a small Go program that prints the parse tree or the
   export as JSON, and compare with Rust's output for each input.
6. **Browser.** With the Rust bridge loaded, the existing Vitest suites in
   `web/src/crdt/` (`core.test.ts`, `conformance.test.ts`) and
   `web/src/editor/model.test.ts` must pass unchanged.
