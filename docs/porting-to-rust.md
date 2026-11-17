# Porting guide: Go to Rust

This guide is for porting the Go parts of Skein to Rust quickly and without
behaviour drift. The Go code was written for this (ADR-0012), so most of the
port is mechanical. The parts that need thought are listed first.

The original design for the Rust version (crate layout, document engine,
labs, milestones) is in [design/index.html](design/index.html), with a later
revision adding the agent and Phase 3 in
[design/skein-rust-design.html](design/skein-rust-design.html).
Where it and this guide disagree, the Go code and the ADRs are newer.

This page is the language-level guide. The step-by-step plan is in
[porting/README.md](porting/README.md): the dependency graph of libraries,
platform, generated code and services, the recommended order, a
`PORTING.md` next to each of them (what it does, what to learn first, the
file-by-file order, crates and pitfalls), vertical slices that port one
feature end to end ([porting/slices/](porting/slices/README.md)), and how to
run Go and Rust side by side with the e2e suite as the acceptance test.

## 1. How to port a package

1. **Read the package doc comment** (`doc.go`). It states the algorithm, the
   invariants and any porting notes. Port the doc comment first, as the
   crate's `//!` docs.
2. **Port the types**, using the mapping tables below.
3. **Port `CheckInvariants` before the logic.** It is the safety net for
   everything else.
4. **Port the tests next**, then the logic until they pass. Tests in Go are
   named after behaviour (`TestConcurrentTypingAtSamePositionDoesNotInterleave`);
   keep the names in snake case.
5. **Run the shared fixtures** in `testdata/conformance/<package>/`. Passing
   them is the definition of done.
6. **Cross-check against Go:** for anything with a wire format, encode in one
   language and decode in the other (section 6).
7. **Benchmark** with `criterion` using the same workloads as the Go
   `bench_test.go`, and record both numbers in `docs/PERF.md`.

## 2. Port order

| Order | Go package | Rust crate | Depends on |
|---|---|---|---|
| 1 | `libs/crdt` | `skein-crdt` | none |
| 2 | `libs/dp` | `skein-dp` | none |
| 3 | `libs/graph` | `skein-graph` | none |
| 4 | `libs/blocks` (block markers, Markdown) | `skein-blocks` | crdt |
| 5 | `libs/trigram` | `skein-trigram` | dp |
| 6 | service domain and app layers | one crate per service | the libraries |
| 7 | service adapters (gRPC, Postgres, Pub/Sub) | in the service crates | tonic, sqlx, google-cloud-pubsub |

Steps 6 and 7 also need the generated crate and the platform crates; the
full order, with `gen/` and `platform/` placed, is in
[porting/README.md](porting/README.md#recommended-order).

Python services (`ml`, `agent`) are not part of the port. If you want them
in Rust too, `linfa` covers classical ML, `candle` or `ort` covers
embeddings, and agent frameworks are young; check the current options first.

## 3. Language mapping

| Go | Rust | Notes |
|---|---|---|
| `type ReplicaID uint64` | `pub struct ReplicaId(pub u64);` | newtypes for every ID |
| struct with `Kind` tag and per-kind fields (`Op`) | `enum` with data per variant | match exhaustively; delete the "unused fields must be zero" checks the enum makes impossible |
| `var ErrX = errors.New(...)` plus `fmt.Errorf("%w: ...")` | `#[derive(thiserror::Error)] enum Error { #[error("...")] X { detail: String } }` | test with `matches!` where Go uses `errors.Is` |
| `func F() (T, error)` | `fn f() -> Result<T, Error>` | |
| `(value, ok bool)` returns | `Option<T>` | |
| small interface used by one package | trait | static dispatch unless it is a port injected at startup (`Arc<dyn Port>`) |
| pointer-linked structures (`*node`) | arena: `Vec<Node>` with `NodeIdx(u32)` | avoids `Rc<RefCell<_>>`; same algorithm |
| `map[K]V` where order matters | `BTreeMap<K, V>` | Go code sorts keys before use; `BTreeMap` makes that implicit |
| `map[K]V` where order never matters | `HashMap<K, V>` | |
| `[]T` | `Vec<T>` | |
| `string` (UTF-8 bytes) | `String` / `&str` | both are UTF-8 |
| `rune` | `char` | both are Unicode scalar values |
| `for _, r := range s` | `for c in s.chars()` | |
| `utf8.RuneCountInString(s)` | `s.chars().count()` | |
| `int` | `usize` for lengths and indexes, `i64` otherwise | never put `usize` in a wire format |
| `uint64` in formats | `u64` | |
| `encoding/binary` uvarint | `leb128` crate or a 15-line function | must reject non-minimal encodings (spec section 7) |
| `math/rand/v2` PCG | `rand_pcg::Pcg64` | seeds are not compatible across languages; fixtures, not seeds, are shared |
| `testing` table tests | `#[test]` with a `for case in cases` loop, or `rstest` | |
| `testing.F` fuzzing | `cargo-fuzz` (libFuzzer) | port the seed corpus inputs |
| property tests with seeded loops | `proptest` | shrinking finds smaller failing cases than Go does |
| goroutine plus channel actor | Tokio task plus `mpsc` | one actor per note in `documents` |
| `context.Context` cancellation | `tokio_util::sync::CancellationToken` | |
| `sync.Mutex` | `std::sync::Mutex` (or `parking_lot`) | never hold across `.await` |
| `log/slog` | `tracing` | |
| gRPC (`google.golang.org/grpc`) | `tonic` + `prost` | same proto files |
| `database/sql` / `pgx` | `sqlx` | runtime-checked queries, as in genuine.dev |

## 4. Things that differ and bite

- **Map iteration order.** Go randomises it; nothing in `libs/` depends on it.
  If you find Go code that iterates a map to produce output, that is a Go bug;
  report it.
- **Integer overflow.** Go wraps silently; Rust panics in debug builds. Where
  Go code adds counters, the values are bounded by `u64`; use `checked_add`
  where the bound is not obvious.
- **Slicing strings.** Go `s[i:j]` slices bytes and never panics on a char
  boundary; Rust `&s[i..j]` panics if `i` or `j` is inside a character. The
  engine indexes by code point, so convert with `char_indices`.
- **Zero values.** Go structs are always fully initialised with zeros. Rust
  has no implicit zero; `#[derive(Default)]` where Go relies on it.
- **Nil versus empty.** A nil Go slice or map behaves as empty; port as empty
  (`Vec::new()`), never as `Option`.
- **Panics as assertions.** The few Go `panic` calls mark unreachable states
  (for example `visibleAt` out of range). Port them as `unreachable!` with the
  same message, not as errors.
- **Equality.** Go compares structs with `==` field by field. Derive
  `PartialEq, Eq` on the same types.

## 5. Per-package notes

### `libs/crdt` (`skein-crdt`)

- `Sequence` becomes a struct holding `nodes: Vec<Node>`,
  `index: HashMap<Id, NodeIdx>`, `vv: BTreeMap<ReplicaId, u64>`,
  `log: Vec<Op>` and `log_pos: HashMap<Id, usize>`.
- `Node` holds `parent: Option<NodeIdx>`, `left: Vec<NodeIdx>`,
  `right: Vec<NodeIdx>`, `prev: Option<NodeIdx>`, `next: Option<NodeIdx>`.
- `Op` becomes `struct Op { id, lamport, body: OpBody }` with
  `enum OpBody { Insert { parent, side, value: char }, Delete { target } }`.
  `Op::validate` loses the checks that zero fields are zero.
- `CausalBuffer` ports as is; `Deliver(ops ...Op)` becomes
  `deliver(&mut self, ops: impl IntoIterator<Item = Op>) -> Result<(), Error>`.
- The codec must match the golden bytes in `codec_test.go` and the spec.

### `libs/graph` (`skein-graph`)

- `Graph` holds `ids: Vec<String>` (ascending), `index: HashMap<String,
  usize>`, `root: Vec<bool>`, `out`, `in_`, `adj: Vec<Vec<usize>>` and the
  weight vectors. Node positions are `usize` and never leave the crate.
- `Direction` becomes `enum Direction { CitedBy, Cites, Undirected }` with
  `CitedBy` as `Default`; the "unknown direction" error disappears.
- `container/heap` becomes `BinaryHeap<Reverse<usize>>`; Dijkstra's
  `costItem` needs a manual `Ord` comparing `(cost, hops, node)` with
  `f64::total_cmp` (costs are never NaN).
- Keep every search iterative (explicit frame stacks); Rust test threads have
  2 MiB of stack and the Go deep-chain test caps its own stack to match.
- Add floating-point values in the same order as Go (PageRank pulls from
  in-links in ascending order; modularity sums communities in label order) so
  results agree to the last bit where they can; fixtures still allow `1e-9`.
- `docs/spec/graph.md` is normative, including the Louvain label rule.

### `libs/blocks` (`skein-blocks`)

- `docs/spec/blocks.md` is normative; the fixtures in
  `testdata/conformance/blocks` cover inline parsing, Markdown import and
  export, attribute registers (with golden bytes) and layout.
- `Node` becomes an enum with one variant per `NodeKind`, each holding its
  span; `AttrWrite` becomes a struct with `value: AttrValue`
  (`Type(Type) | Indent(u32) | Checked(bool) | Language(String)`), which
  deletes `Validate`'s "fields of another key" checks.
- Parse over `Vec<char>`; spans are char indexes. `matchEmphasis` keeps its
  per-character linked lists as `Vec<Delim>` with `prev`/`next: Option<usize>`.
- `Elements` relabels inserts with `char::from_u32(k)` for `k < 0xD800`,
  else `k + 0x800`; port it exactly, or drop it once the Rust CRDT exposes an
  element iterator (then compare its output with the relabelled replay in a
  test).
- `Attributes` keeps `writes: Vec<AttrWrite>`, `seen: HashSet<AttrWrite>`
  and `winners: HashMap<(Id, AttrKey), AttrWrite>`; `Wins` compares
  `(lamport, replica)` then the encoded value bytes.

## 6. Cross-language checks

- **Fixtures:** each Rust crate's tests load `testdata/conformance/<name>`
  with the same semantics as the Go `conformance_test.go`.
- **Wire compatibility:** `make crosscheck` (added with the first Rust crate)
  generates random histories in Go, writes them as encoded batches to
  `testdata/crosscheck/`, and the Rust tests decode, replay and compare the
  final text and version vector. Then the other way round.
- **Golden bytes:** any change to an encoding updates the spec, the Go golden
  test and the Rust golden test in the same pull request.

## 7. Checklist per ported package

- [ ] Crate docs ported from `doc.go`, including invariants
- [ ] `check_invariants` ported and called after every operation in tests
- [ ] All Go unit tests ported with the same names (snake case)
- [ ] Shared fixtures pass
- [ ] Fuzz targets ported with the Go seed corpus
- [ ] Golden bytes match (if the package has a format)
- [ ] Cross-language replay passes (if the package has a format)
- [ ] Benchmarks recorded next to the Go numbers
