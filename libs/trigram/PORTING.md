# Porting `libs/trigram` to Rust (`skein-trigram`)

A trigram inverted index over blocks with roaring bitmap posting lists:
the candidate-selection engine behind literal, fuzzy and regular
expression search, with code and language filters, case-sensitive and
whole-word modes, snippets, and a binary snapshot format. Of the five
libraries this one sits closest to database internals (an inverted index,
compressed bitmaps, a query planner in miniature), and its Rust port has
the most behaviour that differs between Go's and Rust's standard tools.

Normative spec: [`docs/spec/trigram.md`](../../docs/spec/trigram.md).
Decisions: [ADR-0010](../../docs/adr/0010-search-and-discovery.md),
[ADR-0003](../../docs/adr/0003-text-positions.md),
[ADR-0007](../../docs/adr/0007-offline-wasm-client.md) (offline search with a
local trigram index).
General rules: [`docs/porting-to-rust.md`](../../docs/porting-to-rust.md).
The regex engine differences have their own page:
[`docs/porting/libs/trigram/go-regexp-vs-rust-regex.md`](../../docs/porting/libs/trigram/go-regexp-vs-rust-regex.md).

## 1. What it is and why Skein needs it

Search runs in two steps. A query over trigrams (three consecutive folded
code points) is evaluated against posting lists to get candidate blocks
that might match; then only the candidates' texts are read to confirm.
Regular expressions are turned into trigram queries by Russ Cox's analysis,
the method of Google Code Search and zoekt (ADR-0010). Case folding maps
every code point to the smallest member of its simple case-folding orbit,
so one index serves case-sensitive and case-insensitive search. Filters
(code only, languages, the caller's readable-notes bitmap) are intersected
before any text is read, so permission filtering happens before ranking.

| Consumer | What it uses | Feature |
|---|---|---|
| `services/search/internal/domain/workspace.go` | `Index`, `Doc`, `Bitmap`, `Or` | the block index per workspace, updated from `BlocksChanged` events; per-note block bitmaps for permission filters |
| `services/search/internal/domain/textsearch.go`, `regexsearch.go`, `internal/app/search.go` | `SearchLiteral`, `CompileRegexp`, `SearchCompiled`, `Options`, `Span`, `MakeSnippet`, `NormalizeLanguage` | the `Search` RPC: literal and regex code search, `lang:` filters, case sensitivity, whole word, snippets; `candidates` and `matched` counts shown to users |
| `services/search/internal/domain/titles.go` | `FuzzyCandidates` over a title index | `SuggestTitles` (then `dp.DamerauLevenshtein`) |
| `services/search/internal/domain/wordindex.go` | `FuzzyCandidates` over a vocabulary index | typo-tolerant terms (then `dp.LevenshteinWithin`) |
| `services/search/internal/domain/snapshot.go` | `MarshalBinary`, `UnmarshalIndex` | persisting the index so a restart does not re-read every note (`docs/PERF.md`) |
| the browser (planned, ADR-0007) | `Index` and search | offline search on the local replica |

## 2. Learn first

| # | Source | What to take from it | Maps to | Time |
|---|---|---|---|---|
| 1 | Russ Cox, ["Regular Expression Matching with a Trigram Index, or How Google Code Search Worked"](https://research.swtch.com/regexp4) (2012) | The whole design: trigram posting lists, the query algebra, and the regex analysis with `emptyable`, `exact`, `prefix`, `suffix` and `match` per subexpression. The single most important reading | `doc.go`, `query.go`, `regexp.go` | 2 h |
| 2 | Russ Cox's earlier articles in the same series ([index](https://swtch.com/~rsc/regexp/)), especially ["Regular Expression Matching Can Be Simple And Fast"](https://swtch.com/~rsc/regexp/regexp1.html) | Why RE2-style engines (Go `regexp`, Rust `regex`) guarantee linear time, and what leftmost-first matching means | `search.go` (confirmation) | 1.5 h |
| 3 | [google/codesearch](https://github.com/google/codesearch), package [`index`](https://pkg.go.dev/github.com/google/codesearch/index) (`regexp.go`: `RegexpQuery`, `Query` with `QAll`, `QNone`, `QAnd`, `QOr`) | The reference code Skein's `regexp.go` follows, including `maxExact = 7` and `maxSet = 20`. Read it side by side with Skein's version, which works on folded code points instead of bytes | `regexp.go`, `query.go` | 2 h |
| 4 | [sourcegraph/zoekt](https://github.com/sourcegraph/zoekt) ("Fast trigram based code search"; [design doc](https://gerrit.googlesource.com/zoekt/+/52bf92747c61b56f07403ef4f6709249399a25c1/doc/design.md)) | A production trigram code search engine: how it shards, ranks and confirms; context for later work | `doc.go` | 1 h (optional) |
| 5 | Chambi, Lemire, Kaser, Godin, ["Better bitmap performance with Roaring bitmaps"](https://arxiv.org/abs/1402.6407) (Software: Practice and Experience, 2016) | Chunks of 2^16 values, array containers up to 4096 values, bitset containers above, and the set operations between them | `bitmap.go` | 2 h |
| 6 | Lemire et al., ["Roaring Bitmaps: Implementation of an Optimized Software Library"](https://arxiv.org/abs/1709.07821v4) (2017) | How CRoaring makes the container operations fast (galloping intersections, SIMD); read after your port works, for the optimisation pass | `bitmap.go` | 1 h (optional) |
| 7 | [RoaringFormatSpec](https://github.com/roaringbitmap/roaringformatspec) | The portable serialization byte by byte: cookie 12346 (no run containers) or 12347, the key and cardinality-minus-one header, the offset header, little-endian containers. Skein writes 12346 only | `bitmap.go` (`MarshalBinary`, `decodeBitmap`), spec section 9 | 0.5 h |
| 8 | [roaring-rs](https://github.com/RoaringBitmap/roaring-rs) (crate [`roaring`](https://docs.rs/roaring); [`serialize_into`](https://docs.rs/roaring/0.8.0/roaring/bitmap/struct.RoaringBitmap.html)) | The pure Rust roaring crate; its `serialize_into` and `deserialize_from` use the same portable format, so it is a ready-made oracle for your bitmap's bytes | `bitmap.go`, tests | 0.5 h |
| 9 | Esko Ukkonen, "Approximate string-matching with q-grams and maximal matches" (Theoretical Computer Science 92(1), 1992; [bibliographic entry](https://ftp.math.utah.edu/pub/tex/bib/idx/tcs1990/92/1/191_211.html)) | The q-gram count filter: an edit destroys at most q of a string's q-grams, so a near match shares at least `|T| - q·k` of them. Skein uses 4 per edit because a transposition can destroy four | `search.go` (`FuzzyCandidates`, `MinSharedTrigrams`) | 1 h |
| 10 | Go [`regexp/syntax`](https://pkg.go.dev/regexp/syntax) and Rust [`regex`](https://docs.rs/regex/=1.8) docs, plus the [`regex-syntax` HIR](https://docs.rs/regex-syntax/0.8.8/regex_syntax/hir/enum.HirKind.html) | Where the two engines differ: ASCII versus Unicode `\d \s \w \b`, `Simplify` versus `Repetition`, folded literals versus classes. Summarised in the [regex differences page](../../docs/porting/libs/trigram/go-regexp-vs-rust-regex.md) | `regexp.go`, `search.go`, `fold.go` | 1.5 h |

Reading order: 1, 3 (the core, one long evening), 5, 7, 8 (bitmaps), 9,
10 and the differences page, then 2, 4 and 6 for depth. About 10 hours,
plus 4 optional.

## 3. Port order

Types and invariant checkers first, then the logic, then the two binary
formats (bitmap and index snapshot), then fuzzing and benchmarks.

| # | Go file | Contents | Needs | Invariants and rules | Done when |
|---|---|---|---|---|---|
| 1 | `doc.go` | package docs | none | | `lib.rs` `//!` docs |
| 2 | `errors.go` | `ErrDuplicateDoc`, `ErrUnknownDoc`, `ErrInvalidPattern`, `ErrInvalidArgument`, `ErrCorrupt`, `ErrInvariant` | none | | the enum in section 5 |
| 3 | `fold.go` | `Fold`, `FoldString`, `Trigram` (`f0 << 42 \| f1 << 21 \| f2`), `MakeTrigram`, `Runes`, `String`, `Trigrams` (distinct, sorted), `IsWordRune`, `asciiLower` | 2 | spec 2 and 3 | `fold_uses_the_smallest_member_of_the_case_orbit`, `trigrams_are_folded_distinct_and_count_code_points`; fixtures 001, 002 |
| 4 | `bitmap.go`, in memory | `container` (array or 1024-word bitset), `Bitmap` (keys, containers), `CheckInvariants` (B1 ascending keys, B2 no empty container, B3 canonical kind) first, then `BitmapOf`, `Add`, `Remove`, `Contains`, `Len`, `IsEmpty`, `Clone`, `And`, `Or`, `AndNot`, `ForEach`, `ToArray`, `Equal` | 2 | B1–B3 after every operation | `bitmap_matches_set_oracle_under_random_operations`, `set_operations_match_the_oracle`, `nil_bitmap_is_empty` (becomes "default bitmap is empty") |
| 5 | `index.go`, in memory | `DocID = u32`, `Doc`, `Index`, `CheckInvariants` (I1–I4) first, then `NewIndex`, `Len`, `TrigramCount`, `Doc`, `All`, `Postings`, `Add`, `Update` (touches only changed trigrams), `Put`, `Remove`, `post`/`unpost`, `filter`/`unfilter` | 3, 4 | I1–I4 | `index_invariants_hold_after_every_add_update_and_remove`, `index_check_invariants_detects_corruption` (an in-crate unit test: it corrupts private maps) |
| 6 | `query.go` | `QueryKind`, `Query`, `CheckInvariants` (Q1–Q4) first, then `AllQuery`, `NoneQuery`, `TrigramQuery`, `AndQuery`, `OrQuery` with the normal form of spec 5.1, `String` (spec 5.2), `Matches`, `Eval` over an index | 3, 4, 5 | the normal form is part of the spec; every constructor returns it | covered by the regexp tests and fixtures 003–009 (`query` strings) |
| 7 | `regexp.go` | `Regexp` (compiled matcher plus query), `CompileRegexp` (prefix `(?i)` unless case sensitive), `RegexpQuery`, `regexpInfo`, `analyze`, `repeatAtLeastOnce`, `classInfo`, `concat`, `alternate`, `addExact`, `simplify`, `simplifySet`, `andTrigrams`, `stringSet` (`clean`, `union`, `cross`, `minLen`), `compareReversed`, `runePrefix`, `runeSuffix` | 3, 6 | soundness: a matching text always satisfies the query; the limits `maxExact = 7`, `maxSet = 20`, `maxClass = 100` | `regexp_query_of_common_shapes`, `regexp_query_never_rules_out_a_matching_text`; fixtures 003–009. Read the regex differences page first |
| 8 | `search.go` | `Options`, `Span`, `Hit`, `Result`, `filterSet`, `Candidates`, `SearchLiteral`, `literalMatches`, `isWholeWord`, `SearchRegexp`, `SearchCompiled`, `RegexpMatches` (byte to code point offsets), `FuzzyCandidates`, `MinSharedTrigrams`, `NormalizeLanguage` | 5, 6, 7 | spec 6 and 7: filters before counting, leftmost-first non-overlapping matches, whole-word rule | `literal_search_matches_brute_force_scan`, `regexp_search_matches_brute_force_scan`, `search_counts_only_documents_inside_the_filter`, `fuzzy_candidates_never_miss_a_document_with_a_near_substring`, `fuzzy_candidates_exclude_documents_sharing_too_few_trigrams`; fixtures 010–015 |
| 9 | `snippet.go` | `Snippet`, `MakeSnippet` over `dp::best_snippet`, `sortedMatches` | 8, `skein-dp` | spec 8 | `snippet_centres_the_best_span_and_clips_highlights`, `snippet_stays_inside_the_text_and_counts_code_points`, `snippet_of_a_match_longer_than_the_window_starts_at_the_match`, `snippet_rejects_matches_outside_the_text`; fixture 016 |
| 10 | `bitmap.go`, codec | `MarshalBinary`, `containerBytes`, `UnmarshalBinary`, `decodeBitmap` (portable format, cookie 12346) | 4 | keys and arrays strictly ascending, canonical kinds, no trailing bytes | `bitmap_encoding_matches_the_portable_roaring_format`, `bitmap_round_trips_through_its_encoding`, `bitmap_decoding_rejects_malformed_input`; fixture 017 |
| 11 | `index.go`, snapshot | `MarshalBinary` (`"SKTG"`, version 1, documents ascending, postings ascending), `UnmarshalIndex`, `reader` | 5, 10 | spec 9; every posting non-empty and naming known documents | `snapshot_round_trips_and_is_deterministic`, `snapshot_decoding_rejects_every_truncation` |
| 12 | tests: `helpers_test.go`, `conformance_test.go`, `oracle_test.go` | seeded RNG, random texts, docs and regexps, the brute-force scan oracle, the fixture runner | all | | every file in `testdata/conformance/trigram/` |
| 13 | fuzz: `FuzzBitmapUnmarshal`, `FuzzUnmarshalIndex`, `FuzzRegexpQuery` | decoders never panic; the regex query is sound on random texts | 7, 10, 11 | | `cargo fuzz` targets run clean, seeded with the Go `f.Add` inputs |
| 14 | benchmarks: `bench_test.go` | `Add10k`, `Literal10k`, `Regexp10k`, `Snapshot10k` | all | | criterion numbers beside Go's in `docs/PERF.md` |

Fixtures (`testdata/conformance/trigram/`): folding 001, trigrams 002,
queries for literals and regexes 003–009 (008 `\bINC-\d{4}\b` is the
canary for the ASCII-class difference), search and filters 010–015
(013 pins the empty-match rule), snippets 016, bitmap bytes 017.

## 4. Dependencies

Imports: `libs/dp` (`BestSnippet`, `Match`) and the Go standard library,
including `regexp` and `regexp/syntax` (RE2), `unicode` (`SimpleFold`,
`IsLetter`, `IsDigit`, `IsMark`, graphic categories for `String`),
`encoding/binary`, `math/bits`, `slices`, `strings`, `strconv`,
`unicode/utf8`.

| Need | Rust | Note |
|---|---|---|
| snippets | `skein-dp` | path dependency |
| regex matching | [`regex`](https://docs.rs/regex/=1.8) | RE2-style, leftmost-first; Unicode-aware Perl classes by default, unlike Go (see the differences page) |
| regex parsing for the analysis | `regex-syntax` (the same version `regex` uses) | walk the HIR (`Literal`, `Class`, `Look`, `Repetition`, `Capture`, `Concat`, `Alternation`) |
| case folding | `regex_syntax::hir::ClassUnicode::case_fold_simple` on a one-code-point class, then take the minimum | uses the same tables as the regex engine; Go 1.24 is Unicode 15.0, check the version `regex-syntax` ships |
| word code points, graphic categories | `unicode-general-category` | L, Nd, M for words; L, M, N, P, S or U+0020 for `String` |
| bitmaps | write `Bitmap` yourself, test it against `roaring` | the hand-written one is about 600 lines of Go and is the place to learn the structure; the [`roaring`](https://docs.rs/roaring) crate reads and writes the same portable format, so use it as a byte-for-byte oracle. Switching to it later is fine if it never emits run containers into Skein snapshots (cookie 12347 is rejected by the Go decoder) |
| uvarint, little-endian | write them (or `u32::from_le_bytes`) | minimal varints only |
| property tests, fuzzing, benchmarks | `proptest`, `cargo-fuzz`, `criterion` | |

What depends on it: the search service crate; later the WASM bridge if
offline search lands.

## 5. Rust layout

Crate `skein-trigram`, edition 2024, `#![forbid(unsafe_code)]`.

```
skein-trigram/
  src/
    lib.rs        //! from doc.go
    error.rs
    fold.rs       fold, fold_string, Trigram, trigrams, is_word_char
    bitmap/
      mod.rs      Bitmap, Container, set operations, check_invariants
      codec.rs    portable roaring format
    index/
      mod.rs      Doc, Index, add/update/put/remove, check_invariants
      snapshot.rs SKTG format
    query.rs      Query, normal form, Display, matches, eval
    regexp/
      mod.rs      Regexp, compile_regexp, regexp_query
      analyze.rs  RegexpInfo, concat, alternate, simplify, StringSet
      normalize.rs  Go-compatible rewriting of the regex-syntax HIR (see the differences page)
    search.rs     Options, Span, Hit, SearchResult, literal, regexp, fuzzy
    snippet.rs
  tests/{conformance.rs, oracle.rs, bitmap.rs, index.rs, regexp.rs, snippet.rs}
  fuzz/fuzz_targets/{bitmap_unmarshal.rs, unmarshal_index.rs, regexp_query.rs}
  benches/trigram.rs
```

Public API sketch:

```rust
pub fn fold(c: char) -> char;
pub fn fold_string(s: &str) -> String;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct Trigram(pub u64);
impl Trigram {
    pub fn new(a: char, b: char, c: char) -> Self; // folded code points
    pub fn chars(self) -> [char; 3];
}
impl std::fmt::Display for Trigram { /* spec 5.2 quoting */ }
pub fn trigrams(s: &str) -> Vec<Trigram>;          // distinct, ascending
pub fn is_word_char(c: char) -> bool;

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Bitmap { keys: Vec<u16>, containers: Vec<Container> }
enum Container { Array(Vec<u16>), Bitset { words: Box<[u64; 1024]>, len: u32 } }
impl Bitmap {
    pub fn add(&mut self, v: u32) -> bool;
    pub fn remove(&mut self, v: u32) -> bool;
    pub fn contains(&self, v: u32) -> bool;
    pub fn len(&self) -> u64;
    pub fn is_empty(&self) -> bool;
    pub fn iter(&self) -> impl Iterator<Item = u32> + '_;
    pub fn and(&self, other: &Bitmap) -> Bitmap;
    pub fn or(&self, other: &Bitmap) -> Bitmap;
    pub fn and_not(&self, other: &Bitmap) -> Bitmap;
    pub fn check_invariants(&self) -> Result<(), Error>;
    pub fn to_bytes(&self) -> Vec<u8>;
    pub fn from_bytes(data: &[u8]) -> Result<Bitmap, Error>;
}
impl FromIterator<u32> for Bitmap { /* BitmapOf */ }

pub type DocId = u32;
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Doc { pub text: String, pub code: bool, pub language: String }

pub struct Index { /* docs: HashMap<DocId, Doc>, postings: HashMap<Trigram, Bitmap>, live, code: Bitmap, languages: HashMap<String, Bitmap> */ }
impl Index {
    pub fn new() -> Self;
    pub fn len(&self) -> usize;
    pub fn trigram_count(&self) -> usize;
    pub fn doc(&self, id: DocId) -> Option<&Doc>;
    pub fn all(&self) -> Bitmap;
    pub fn postings(&self, t: Trigram) -> Option<&Bitmap>;
    pub fn add(&mut self, id: DocId, doc: Doc) -> Result<(), Error>;
    pub fn update(&mut self, id: DocId, doc: Doc) -> Result<(), Error>;
    pub fn put(&mut self, id: DocId, doc: Doc);
    pub fn remove(&mut self, id: DocId) -> Result<(), Error>;
    pub fn check_invariants(&self) -> Result<(), Error>;
    pub fn to_bytes(&self) -> Vec<u8>;
    pub fn from_bytes(data: &[u8]) -> Result<Index, Error>;

    pub fn candidates(&self, q: &Query, opts: &Options) -> Bitmap;
    pub fn search_literal(&self, literal: &str, opts: &Options) -> SearchResult;
    pub fn search_regexp(&self, pattern: &str, opts: &Options) -> Result<SearchResult, Error>;
    pub fn search_compiled(&self, re: &Regexp, opts: &Options) -> SearchResult;
    pub fn fuzzy_candidates(&self, term: &str, max_edits: usize, opts: &Options) -> Bitmap;
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Query { All, None, And { trigrams: Vec<Trigram>, subs: Vec<Query> }, Or { trigrams: Vec<Trigram>, subs: Vec<Query> } }
impl Query {
    pub fn literal(s: &str) -> Query;
    pub fn and(a: Query, b: Query) -> Query;
    pub fn or(a: Query, b: Query) -> Query;
    pub fn matches(&self, has: impl Fn(Trigram) -> bool) -> bool;
    pub fn eval(&self, ix: &Index) -> Bitmap;
    pub fn check_invariants(&self) -> Result<(), Error>;
}
impl std::fmt::Display for Query { /* spec 5.2 */ }

pub struct Regexp { re: regex::Regex, query: Query }
pub fn compile_regexp(pattern: &str, case_sensitive: bool) -> Result<Regexp, Error>;
pub fn regexp_matches(re: &Regexp, text: &str, whole_word: bool) -> Vec<Span>;

#[derive(Clone, Debug, Default)]
pub struct Options { pub case_sensitive: bool, pub whole_word: bool, pub code_only: bool, pub languages: Vec<String>, pub within: Option<Bitmap> }
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Span { pub start: usize, pub end: usize }
pub struct Hit { pub doc: DocId, pub matches: Vec<Span> }
pub struct SearchResult { pub candidates: usize, pub hits: Vec<Hit> }

pub fn min_shared_trigrams(term: &str, max_edits: usize) -> i64; // may be zero or negative
pub fn normalize_language(lang: &str) -> String;

pub struct Snippet { pub text: String, pub window: Span, pub highlights: Vec<Span>, pub focus: Span }
pub fn make_snippet(text: &str, matches: &[skein_dp::Match], window: usize) -> Result<Snippet, Error>;
```

`Options.within` is `Option<Bitmap>` because Go's `nil` (no restriction)
and an empty bitmap (nothing readable) mean different things there; this
is the one place where nil versus empty is a signal, so keep the
distinction explicit. Go's `FuzzyCandidates` returns `ErrInvalidArgument`
for negative edits; `usize` makes that unrepresentable.

Error enum:

```rust
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum Error {
    #[error("trigram: duplicate document id {0}")]  DuplicateDoc(DocId),
    #[error("trigram: unknown document id {0}")]    UnknownDoc(DocId),
    #[error("trigram: invalid pattern: {0}")]       InvalidPattern(String),
    #[error("trigram: invalid argument: {0}")]      InvalidArgument(String),
    #[error("trigram: corrupt encoding: {0}")]      Corrupt(String),
    #[error("trigram: invariant violated: {0}")]    Invariant(String),
}
```

Feature flags: `regex` (default on). With it off, the crate has the
index, literal and fuzzy search but no `Regexp`, which keeps a WASM build
for offline literal search small. WASM: no `wasm-bindgen` in this crate;
if offline search lands, expose `Index` through the bridge crate with
handle-based functions like the CRDT ones, converting code point offsets to
UTF-16 in TypeScript as `web/src/crdt/utf16.ts` already does.

## 6. Pitfalls and cross-checking

**Go's `regexp` and Rust's `regex` are not the same language.** Perl
classes and word boundaries are ASCII in Go and Unicode in Rust; Go
simplifies counted repetitions and keeps `(?i)` literals as one folded
literal, while `regex-syntax` keeps `Repetition` nodes and turns `(?i)`
letters into classes; the parsers factor alternations differently; the
parse limits differ. Each changes either which texts match or which query
the analysis builds. The
[differences page](../../docs/porting/libs/trigram/go-regexp-vs-rust-regex.md)
lists each one with its fix. Fixture 008 (`\bINC-\d{4}\b`) fails if `\d`
is Unicode.

**Offsets.** All results are code point offsets. `regex` returns byte
offsets; convert them in one ascending pass as `RegexpMatches` does, and
never slice a `&str` at a code point index.

**Invalid UTF-8.** The spec decodes each invalid byte to one U+FFFD (Go's
`range`). Block texts reach the index from protobuf strings, which are
valid UTF-8, so a `&str` API never sees invalid bytes. The one place this
can bite is `from_bytes` on a snapshot: decode text with `str::from_utf8`
and reject the snapshot as corrupt on failure, rather than
`from_utf8_lossy`, which would silently change offsets (Go writes only
valid text, so a valid snapshot never fails).

**Case folding.** `fold` is the smallest member of the simple folding
orbit, not `to_lowercase` or `to_uppercase` (`k`, `K` and U+212A all fold
to `K`; `ς`, `σ`, `Σ` to `Σ`). Fixtures 001, 010 and 011 pin it. The
Unicode version must match Go 1.24's 15.0 for the fold fixtures.

**Trigram encoding.** `f0 << 42 | f1 << 21 | f2` with 21 bits per code
point; sorting by the `u64` sorts by code points. Use `u64` arithmetic,
never `u32`.

**Bitmaps.** Containers are canonical: an array iff cardinality <= 4096
(B3). Converting at exactly 4096 versus 4097 is the classic off-by-one;
`bitmap_encoding_matches_the_portable_roaring_format` and fixture 017
catch it. The decoder rejects non-ascending keys or array values,
non-canonical kinds, cookie 12347 and trailing bytes, and must not
allocate from an unchecked container count.

**Nil versus empty.** Go's nil `*Bitmap` is empty everywhere except
`Options.Within`. Port nil bitmaps as `Bitmap::default()` and `Within` as
`Option<Bitmap>`.

**Map iteration.** Hits are by ascending document ID (iterate the
candidate bitmap, which is sorted). `FuzzyCandidates` counts in a map and
adds the qualifying IDs to a bitmap, so the result is sorted whatever the
map order. Snapshots write documents and trigrams ascending: sort the
`HashMap` keys (or keep `BTreeMap`s) before encoding.

**Recursion.** `analyze` recurses over the regex tree. Go's parser caps
nesting at 1000; `regex-syntax`'s default nest limit is lower (250), so
either raise it to 1000 with `ParserBuilder::nest_limit` to accept what Go
accepts, or document the difference. Rust's 2 MiB test stack handles 1000
levels of `analyze` if frames stay small.

Cross-checking against Go:

1. **Fixtures.** `tests/conformance.rs` runs `testdata/conformance/trigram/*.json`
   (queries compared by their spec 5.2 string form, hits and counts
   exactly, bitmap bytes in hex).
2. **Brute-force oracles.** Port `oracle_test.go`: literal and regex search
   against a scan of every document, fuzzy candidates against a sliding
   Damerau–Levenshtein check (`skein-dp`).
3. **Bitmap bytes against `roaring`.** For random sets, your `to_bytes`
   must equal `roaring::RoaringBitmap::serialize_into` byte for byte (both
   canonical, no runs), and each must decode the other.
4. **Snapshots across languages.** `BenchmarkSnapshot10k`'s index written
   by Go (`testdata/crosscheck/trigram/index-10k.sktg`, generated once)
   must decode in Rust, pass `check_invariants`, and re-encode to the same
   bytes; then the other way round.
5. **Regex query corpus.** The highest-value check: a Go program writes
   `pattern, case_sensitive, query_string` for thousands of patterns (the
   `randomRegexp` generator in `helpers_test.go` plus the patterns in the
   fixtures, the seed data's search truth and the benchmarks) to
   `testdata/crosscheck/trigram/regex-queries.jsonl`; the Rust test
   compares `regexp_query(...).to_string()` for each line. Every
   difference is either a bug in the normalisation layer or a case for the
   differences page.
6. **Search truth.** The search service's tests use the small seed's
   `qrels`; once the service crate is ported, the same queries must
   return the same candidates and matches.
