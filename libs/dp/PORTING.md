# Porting `libs/dp` to Rust (`skein-dp`)

Skein's dynamic programming algorithms: edit distances, longest common
subsequence, diffs, a reading-plan solver, optimal chunking, snippet
windows and word segmentation. Pure functions over inputs, with every tie
broken by a written rule, so the Rust port must return exactly the same
answer, not just an equally good one.

Normative spec: [`docs/spec/dp.md`](../../docs/spec/dp.md).
Decisions: [ADR-0011](../../docs/adr/0011-graph-and-dp-libraries.md),
[ADR-0003](../../docs/adr/0003-text-positions.md).
General rules: [`docs/porting-to-rust.md`](../../docs/porting-to-rust.md).

## 1. What it is and why Skein needs it

| Algorithm | Go | Why Skein needs it | Used by |
|---|---|---|---|
| Levenshtein, banded `LevenshteinWithin` | `edit.go` | typo-tolerant search terms: confirm a fuzzy candidate within k edits, stopping early | `services/search/internal/domain/wordindex.go` (fuzzy terms in `Search`) |
| Damerau–Levenshtein (unrestricted) | `edit.go` | title suggestions and fuzzy terms; it is a metric, so a BK-tree can prune by the triangle inequality | `services/search/internal/domain/titles.go` (`SuggestTitles`), `wordindex.go` |
| LCS (Hirschberg) | `lcs.go` | the building block behind diffs; no production caller yet | tests, fixtures |
| Myers diff, patience diff, patches | `diff.go`, `patience.go`, `text.go` | note history: what changed between two versions | `services/documents/internal/domain/history.go` (`DiffText`: patience over lines, then a character diff inside each changed run), serving the `Diff` RPC; `ApplyTextPatch` checks round trips in documents' tests |
| Reading plan (precedence-constrained knapsack) | `plan.go`, `plan_solve.go` | "what should I read in 60 minutes to learn this topic" with prerequisites | `services/search/internal/app/graph.go` (`ReadingPlan` RPC) |
| Optimal chunking | `chunk.go` | split a note into embedding chunks of at most L tokens, evenly | no Go caller yet (the Python `ml` service chunks today) |
| Best snippet | `snippet.go` | the span of a search hit covering the most distinct query terms | `services/search/internal/domain/regexsearch.go`, and `libs/trigram`'s `MakeSnippet` |
| Word segmentation (Viterbi, unigram model) | `segment.go` | split "rustasync" into "rust async" for queries typed without spaces | no Go caller yet |

The hunk kinds (`equal` 1, `insert` 2, `delete` 3) are the values of
`DiffHunk.Kind` in `proto/skein/v1/documents.proto`.

## 2. Learn first

| # | Source | What to take from it | Maps to | Time |
|---|---|---|---|---|
| 1 | [Damerau–Levenshtein distance](https://en.wikipedia.org/wiki/Damerau%E2%80%93Levenshtein_distance) (Wikipedia) | Levenshtein's recurrence, then the difference between optimal string alignment and the unrestricted distance (`CA` to `ABC` is 3 versus 2) and the `da` array algorithm | `edit.go` | 1 h |
| 2 | Roy Lowrance and Robert A. Wagner, ["An Extension of the String-to-String Correction Problem"](https://www.lemoda.net/text-fuzzy/lowrance-wagner/index.html) (JACM 22(2), 1975) | The original unrestricted recurrence with transpositions; `damerauLevenshtein` follows it with an extra sentinel row and column | `edit.go` (`DamerauLevenshtein`) | 1 h |
| 3 | Juha Kärkkäinen's [approximate string matching lecture notes](https://www.cs.helsinki.fi/u/tpkarkka/teach/15-16/SPA/lecture06.pdf) (Helsinki) | Ukkonen's cut-off: only diagonals within k of the main one can stay at or below k, which is the band in `LevenshteinWithin` | `edit.go` (`levenshteinWithin`) | 1 h |
| 4 | D. S. Hirschberg, ["A Linear Space Algorithm for Computing Maximal Common Subsequences"](https://ics.uci.edu/~dhirschb/pubs/p341-hirschberg.pdf) (CACM 18(6), 1975) | Split `a` in half, run forward and backward score rows, choose the split of `b`, recurse. Skein fixes the tie: the smallest `j` | `lcs.go` | 1 h |
| 5 | Eugene W. Myers, ["An O(ND) Difference Algorithm and Its Variations"](https://pubs.dbs.uni-leipzig.de/se/node/609) (Algorithmica 1(2), 1986) | The edit graph, furthest-reaching D-paths on diagonals, and section 4b's middle snake for linear space. Spec section 4 pins the exact matching, including which pass tests overlap | `diff.go` (`middleSnake`, `script.diff`) | 3 h |
| 6 | James Coglan, ["The Myers diff algorithm: part 1"](https://blog.jcoglan.com/2017/02/12/the-myers-diff-algorithm-part-1/), [part 2](https://blog.jcoglan.com/2017/02/15/the-myers-diff-algorithm-part-2/), ["Myers diff in linear space: theory"](https://blog.jcoglan.com/2017/03/22/myers-diff-in-linear-space-theory/) and [implementation](https://blog.jcoglan.com/2017/04/25/myers-diff-in-linear-space-implementation/) | The same algorithm drawn step by step; read before or alongside the paper | `diff.go` | 2 h |
| 7 | James Coglan, ["The patience diff algorithm"](https://blog.jcoglan.com/2017/09/19/the-patience-diff-algorithm) and ["Implementing patience diff"](https://blog.jcoglan.com/2017/09/28/implementing-patience-diff/) | Bram Cohen's patience diff: anchor on lines unique to both sides, keep the longest non-crossing chain by patience sorting, recurse, fall back to Myers | `patience.go` | 1 h |
| 8 | [mitsuhiko/similar](https://github.com/mitsuhiko/similar) (Rust) | A production Rust diff crate with Myers, patience and LCS over lines, words and chars; useful to compare against, not to depend on (its matching differs from spec section 4) | `diff.go`, `patience.go`, `text.go` | browse |
| 9 | D. S. Johnson and K. A. Niemi, ["On Knapsacks, Partitions, and a New Dynamic Programming Technique for Trees"](https://scilit.com/publications/9319ff67eac3add93a4b5d5ee93e4a6a) (Math. of OR 8(1), 1983) | The tree knapsack DP in preorder ("skip the subtree or take the node"), which `solveTreeDP` uses for forests | `plan_solve.go` (`solveTreeDP`) | 1.5 h |
| 10 | N. Samphaiboon and Y. Yamada, ["Heuristic and Exact Algorithms for the Precedence-Constrained Knapsack Problem"](https://ideas.repec.org/a/spr/joptap/v105y2000i3d10.1023_a1004649425222.html) (JOTA 105(3), 2000) | The general (DAG) problem and why it is solved by bounding and search; Skein's branch and bound with a fractional-knapsack bound is a simple member of this family | `plan_solve.go` (`solveBranchAndBound`, `bound`, `greedyPlan`) | 1 h (skim) |
| 11 | A. B. Kahn, ["Topological sorting of large networks"](https://scilit.com/publications/1bc5bd2b4676f7e4e83f2fd8d23f7aaf) (CACM 5(11), 1962) | Kahn's algorithm; the plan's reading order is Kahn with "smallest rank first" | `plan.go` (`readingOrder`) | 0.5 h |
| 12 | Knuth and Plass, "Breaking Paragraphs into Lines" (Software: Practice and Experience 11(11), 1981; overview on [Wikiwand](https://www.wikiwand.com/en/articles/Knuth–Plass_line-breaking_algorithm)) | Minimum raggedness: choose breaks minimising the sum of squared slack. `ChunkBlocks` is the same DP over blocks instead of words | `chunk.go` | 1 h |
| 13 | Peter Norvig, "Natural Language Corpus Data" (Beautiful Data, ch. 14; [chapter and code](https://norvig.com/ngrams/)) | The unigram segmentation model and the unknown-word penalty that `NewSegmenter` turns into integer micro-nats | `segment.go` | 1 h |
| 14 | [strsim](https://docs.rs/crate/strsim/latest) (Rust) | Separate `osa_distance` and `damerau_levenshtein`: a good oracle for your `damerau_levenshtein` in tests (as a dev-dependency only) | `edit.go` | browse |

Reading order: 1, 2, 3 (edit distances, an evening), 4, 6, 5, 7 (diffs, a
weekend), then 11, 9, 10 (plans), then 12 and 13. About 16 hours in all.

## 3. Port order

Each result checker (the package's invariants) comes before the algorithm
it checks.

| # | Go file | Contents | Needs | Invariants and rules | Done when |
|---|---|---|---|---|---|
| 1 | `doc.go` | package docs | none | determinism, code points, integer values | `lib.rs` `//!` docs |
| 2 | `errors.go` | `ErrInvalidInput`, `ErrUnknownPrerequisite`, `ErrCyclicPrerequisites` (with `CycleError{Cycle}`), `ErrProblemTooLarge`, `ErrBlockTooLarge`, `ErrPatchMismatch`, `ErrInvariant` | none | | the enum in section 5 |
| 3 | `text.go`, the tokenisers | `Lines`, `Words` (word, space, other classes), `Chars` | none | concatenating tokens gives back the input; an invalid byte is its own token (spec 1) | `tokenisers_are_lossless`, `words_splits_words_spaces_and_punctuation` |
| 4 | `edit.go` | `Levenshtein` (two rows), `LevenshteinWithin` (band), `DamerauLevenshtein` (full matrix, `da` map) | 3 for nothing; standalone | distances count code points; `within` returns `(k+1, false)` past k | `levenshtein_matches_all_alignments_oracle`, `levenshtein_within_equals_full_distance_or_reports_too_far`, `levenshtein_within_stops_early_on_distant_long_strings`, `damerau_levenshtein_matches_edit_graph_oracle`, `damerau_levenshtein_is_unrestricted_not_optimal_string_alignment`, `edit_distances_count_code_points_not_bytes`, `edit_distance_properties`; fixtures 001–005 |
| 5 | `lcs.go` | `LCSLength`, `LCS` (Hirschberg), forward and backward rows | none | smallest `j` maximising `F[j] + B[j]` (spec 3) | `lcs_matches_brute_force_oracle`, `lcs_on_long_inputs_agrees_with_length`, `lcs_breaks_ties_by_smallest_split`; fixture 006 |
| 6 | `diff.go`, types and checker | `HunkKind`, `Hunk<T>`, `CheckHunks`, `ApplyPatch`, `InvertPatch`, `EditCount` | 2 | canonical form (spec 4) | `check_hunks_rejects_non_canonical_diffs`, `apply_patch_rejects_mismatched_hunks` |
| 7 | `diff.go`, Myers | `Diff`, `script` (emit and group), `middleSnake`, `commonPrefix`, `commonSuffix` | 6 | the exact middle-snake rules of spec 4 step 3 | `diff_is_minimal_and_applies_on_random_sequences`, `diff_of_long_similar_sequences_is_minimal`, `diff_puts_deletes_before_inserts_in_each_change`, `diff_of_equal_and_empty_inputs`; fixtures 007, 008, 010 |
| 8 | `patience.go` | `PatienceDiff`, unique common elements, longest increasing chain by piles | 7 | anchors in `a` order; leftmost pile with a larger top | `patience_diff_anchors_on_unique_lines_rather_than_repeated_ones`; fixture 009 |
| 9 | `text.go`, text diffs | `TextHunk`, `DiffLines`, `DiffWords`, `DiffChars`, `PatienceDiffLines`, `ApplyTextPatch`, `joinHunks` | 3, 7, 8 | | `text_diffs_round_trip_at_every_granularity`, `diff_chars_counts_code_points` |
| 10 | `snippet.go` | `Match`, `Span`, `Snippet`, `BestSnippet` (two pointers over matches sorted by end) | 2 | most distinct terms, then shortest, then earliest (spec 7) | `best_snippet_matches_all_windows_oracle`, `best_snippet_prefers_distinct_terms_over_repeats`, `best_snippet_with_nothing_that_fits`; fixtures 019, 020 |
| 11 | `chunk.go` | `Chunking`, `CheckChunks` first, then `ChunkBlocks` | 2 | cost over all chunks including the last; ties: fewer chunks, then lexicographically largest ends (spec 6) | `check_chunks_rejects_invalid_chunkings`, `chunk_blocks_matches_all_splits_oracle`, `chunk_blocks_prefers_even_chunks_to_greedy_filling`, `chunk_blocks_rejects_impossible_input`; fixtures 016–018 |
| 12 | `segment.go` | `Segmenter`, `CheckInvariants` first, then `NewSegmenter`, `WordCost`, `Segment` | 2, 3 | integer micro-nat costs; ties: fewer words, then longest first word (spec 8) | `new_segmenter_rejects_bad_frequencies`, `segment_matches_all_segmentations_oracle`, `segment_splits_run_together_queries`, `segment_keeps_invalid_utf8_bytes` (only if the Rust API takes bytes; see section 6); fixtures 021, 022 |
| 13 | `plan.go` | `Note`, `PlanOptions`, `PlanMethod`, `PlannedNote`, `Plan`, `CheckPlan` first; then `newPlanProblem` (validation), `findCycle`, `rankNotes`, `readingOrder`, `plan` | 2 | cycle reporting, canonical rank and reading order exactly as spec 5 | `check_plan_rejects_infeasible_plans`, `plan_reading_rejects_invalid_input`, `plan_reading_reports_cycle_starting_at_smallest_id`, `plan_reading_of_nothing_is_empty`; fixture 015 |
| 14 | `plan_solve.go` | `PlanReading`, `solveTreeDP`, `noteSet`, `candidate.beats`, `branchAndBound` (`search`, `canTake`, `offerCurrent`, `bound`), `densityOrder`, `greedyPlan`, `closure`, `denser` | 13 | method choice, tie rule 3, node counting and pruning of spec 5 | `plan_reading_matches_exhaustive_search`, `branch_and_bound_agrees_with_tree_dp_on_forests`, `plan_reading_falls_back_to_best_found_at_node_limit`, `plan_reading_takes_prerequisites_for_a_valuable_note`; fixtures 011–014 |
| 15 | tests: `helpers_test.go`, `conformance_test.go` | seeded RNG, the fixture runner over `kind` | all | unknown keys are an error | every file in `testdata/conformance/dp/` passes |
| 16 | fuzz: `fuzz_test.go` | `FuzzDiff` (diff applies and is canonical), `FuzzEditDistance` (metric properties, banded agrees) | 4, 7, 8 | | `cargo fuzz` targets `diff` and `edit_distance` run clean |
| 17 | benchmarks: `bench_test.go` | the workloads in `docs/PERF.md` | all | | criterion numbers recorded next to the Go ones |

If you want the shortest path to something services use, port 1–4, 6–10
and 13–14 first; 5, 11 and 12 have no production caller yet but have
fixtures, so they still count for "done".

Fixtures (`testdata/conformance/dp/`): edit distances 001–005, LCS 006,
diffs 007–010, reading plans 011–015, chunking 016–018, snippets 019–020,
segmentation 021–022.

## 4. Dependencies

Imports: Go standard library only (`slices`, `math`, `math/bits`,
`unicode`, `unicode/utf8`, `container/heap`, `errors`, `fmt`).

| Need | Rust | Note |
|---|---|---|
| errors | `thiserror` | |
| Unicode letter, digit, mark, space for `Words` | `char::is_alphabetic`/`is_numeric` are **not** the same sets as Go's `unicode.IsLetter`/`IsDigit`/`IsMark`. Use [`unicode-general-category`](https://lib.rs/crates/unicode-general-category) (L*, Nd, M*). For spaces, `char::is_whitespace` and Go's `unicode.IsSpace` both use the White_Space property | Unicode version: Go 1.24 is 15.0; that crate ships 16.0 (section 6) |
| min-heap for Kahn | `BinaryHeap<Reverse<usize>>` | |
| bitset of notes | `Vec<u64>` as in Go, or `fixedbitset` | the `hasLowestDifference` rule needs "lowest set bit of a XOR b" |
| natural log | `f64::ln` | see the rounding note in section 6 |
| diff and distance libraries | write them yourself | `similar` and `strsim` choose different matchings and ties; use them only as test oracles |
| property tests, fuzzing, benchmarks | `proptest`, `cargo-fuzz`, `criterion` | |

What depends on it: `skein-trigram` (`BestSnippet` and `Match` in
`MakeSnippet`), the search service crate (distances, snippets, reading
plans), the documents service crate (diffs).

## 5. Rust layout

Crate `skein-dp`, edition 2024, `#![forbid(unsafe_code)]`.

```
skein-dp/
  src/
    lib.rs        //! from doc.go
    error.rs
    text.rs       tokenisers and text diffs
    edit.rs
    lcs.rs
    diff.rs       Hunk, Myers, apply, invert, check
    patience.rs
    snippet.rs
    chunk.rs
    segment.rs
    plan/
      mod.rs      plan.go: types, validation, cycle, rank, reading order, check_plan
      solve.rs    plan_solve.go: tree DP, branch and bound, greedy
  tests/conformance.rs, tests/*.rs (one per Go test file)
  fuzz/fuzz_targets/{diff.rs, edit_distance.rs}
  benches/dp.rs
```

Public API sketch:

```rust
pub fn levenshtein(a: &str, b: &str) -> usize;
pub fn levenshtein_within(a: &str, b: &str, max_distance: usize) -> Option<usize>; // None = "k+1, false"
pub fn damerau_levenshtein(a: &str, b: &str) -> usize;

pub fn lcs_length<T: Eq>(a: &[T], b: &[T]) -> usize;
pub fn lcs<T: Eq + Clone>(a: &[T], b: &[T]) -> Vec<T>;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum HunkKind { Equal = 1, Insert = 2, Delete = 3 }

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Hunk<T> { pub kind: HunkKind, pub elems: Vec<T> }

pub fn diff<T: Eq + Clone>(a: &[T], b: &[T]) -> Vec<Hunk<T>>;
pub fn patience_diff<T: Eq + Hash + Clone>(a: &[T], b: &[T]) -> Vec<Hunk<T>>;
pub fn apply_patch<T: Eq + Clone>(a: &[T], hunks: &[Hunk<T>]) -> Result<Vec<T>, Error>;
pub fn invert_patch<T: Clone>(hunks: &[Hunk<T>]) -> Vec<Hunk<T>>;
pub fn edit_count<T>(hunks: &[Hunk<T>]) -> usize;
pub fn check_hunks<T: Eq>(a: &[T], b: &[T], hunks: &[Hunk<T>]) -> Result<(), Error>;

pub fn lines(s: &str) -> Vec<&str>;
pub fn words(s: &str) -> Vec<&str>;
pub fn chars(s: &str) -> Vec<&str>;
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TextHunk { pub kind: HunkKind, pub text: String }
pub fn diff_lines(a: &str, b: &str) -> Vec<TextHunk>;
pub fn diff_words(a: &str, b: &str) -> Vec<TextHunk>;
pub fn diff_chars(a: &str, b: &str) -> Vec<TextHunk>;
pub fn patience_diff_lines(a: &str, b: &str) -> Vec<TextHunk>;
pub fn apply_text_patch(a: &str, hunks: &[TextHunk]) -> Result<String, Error>;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Match { pub term: usize, pub start: usize, pub end: usize }
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub struct Span { pub start: usize, pub end: usize }
pub struct Snippet { pub span: Span, pub terms: usize, pub matches: Vec<Match> }
pub fn best_snippet(matches: &[Match], window: usize) -> Result<Option<Snippet>, Error>;

pub const MAX_CHUNK_TOKENS: usize = 1 << 20;
pub struct Chunking { pub ends: Vec<usize>, pub cost: i64 }
pub fn chunk_blocks(tokens: &[usize], limit: usize) -> Result<Chunking, Error>;
pub fn check_chunks(tokens: &[usize], limit: usize, c: &Chunking) -> Result<(), Error>;

pub struct Segmenter { /* cost: HashMap<String, i64>, longest_known, unknown_base, unknown_per_char */ }
pub struct Segmentation { pub words: Vec<String>, pub cost: i64 }
impl Segmenter {
    pub fn new(frequencies: &HashMap<String, u64>) -> Result<Self, Error>;
    pub fn word_cost(&self, word: &str) -> i64;
    pub fn segment(&self, text: &str) -> Segmentation;
    pub fn check_invariants(&self) -> Result<(), Error>;
}

pub struct Note { pub id: String, pub minutes: u32, pub value: i64, pub prerequisites: Vec<String> }
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PlanMethod { TreeDp = 1, BranchAndBound = 2, BestFound = 3 }
pub struct PlanOptions { pub node_limit: usize } // 0 selects DEFAULT_NODE_LIMIT = 200_000
pub struct PlannedNote { pub id: String, pub minutes: u32, pub value: i64, pub required_by: Vec<String> }
pub struct Plan {
    pub notes: Vec<PlannedNote>, pub total_minutes: u32, pub total_value: i64,
    pub method: PlanMethod, pub optimal: bool, pub upper_bound: i64, pub nodes_explored: usize,
}
pub fn plan_reading(notes: &[Note], budget: u32, opts: PlanOptions) -> Result<Plan, Error>;
pub fn check_plan(notes: &[Note], budget: u32, plan: &Plan) -> Result<(), Error>;
```

Go's `LevenshteinWithin` returns `(k+1, false)`; returning `Option` is the
idiomatic port, but the fixture runner must then print `distance: k+1,
within: false` for `None`. Negative `k` cannot be expressed with `usize`;
the spec's "negative k is 0" is handled by the caller's type.

Error enum:

```rust
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum Error {
    #[error("dp: invalid input: {0}")]              InvalidInput(String),
    #[error("dp: unknown prerequisite: {0}")]       UnknownPrerequisite(String),
    #[error("dp: prerequisites form a cycle: {cycle:?}")] CyclicPrerequisites { cycle: Vec<String> },
    #[error("dp: problem too large: {0}")]          ProblemTooLarge(String),
    #[error("dp: block exceeds chunk limit: {0}")]  BlockTooLarge(String),
    #[error("dp: patch does not apply: {0}")]       PatchMismatch(String),
    #[error("dp: invariant violated: {0}")]         Invariant(String),
}
```

`CycleError` becomes the `CyclicPrerequisites { cycle }` variant: the Go
`errors.As` target is just a variant with data.

Feature flags: none. WASM: not used by the browser today; the crate stays
WASM-clean because `skein-trigram` (which ADR-0007 may run offline)
depends on it.

## 6. Pitfalls and cross-checking

**Ties are the spec.** Every algorithm has several optimal answers and the
spec picks one: the smallest Hirschberg split, the exact middle-snake
search order and overlap test, deletes before inserts, the canonical rank
for plans, the lexicographically largest chunk ends, the longest first
word. A "correct" textbook implementation will pass the oracles and fail
the fixtures. Implement from spec sections 3 to 8, not from memory.

**Myers details that bite.** Diagonals are negative, so the `V` array is
offset by `max`; in Rust use `isize` for `k` and index with
`(k + offset) as usize`. The overlap test differs for odd and even `delta`
(spec 4 step 3), and the snake returned is the one just extended, mapped to
forward coordinates for the backward pass.

**Recursion depth.** Hirschberg and Myers recurse on halves (depth
O(log n)), which is fine. Branch and bound recurses once per decided note:
depth up to the number of notes. Go grows its stack; a Rust thread does
not. Either cap plans at a few thousand notes (the search service sends
topics of tens to hundreds) or convert `search` to an explicit stack of
`(pos, decision)` frames. Keep take-before-exclude order and count one node
per frame exactly as Go does, or `best-found` plans and `nodes_explored`
will differ (spec 5 says the Rust port must reproduce the search order).

**Integer widths.** Values are `i64` (up to 2^40 each, summed), minutes and
budgets up to 2^20, costs `i64`. The tree DP table has up to
`MaxPlanCells = 2^22` cells; allocate it with the same check. Squared slack
in chunking is at most `(2^20)^2 = 2^40` per chunk: `i64` is enough, `i32`
is not.

**Floating point in segmentation.** Costs are `round(1e6 · ln x)` rounded
half away from zero: Rust `f64::round` already rounds half away from zero,
matching Go's `math.Round`. `ln` may differ from Go's `math.Log` in the
last bit; that changes a rounded cost only when the unrounded value lies
within about 1e-10 of a .5 boundary (the `doc.go` porting note). If a
segmentation fixture ever disagrees by one micro-nat, this is the cause.
Do not keep float costs: compare and sum the integers.

**Unicode tables.** `Words` (and `libs/trigram`'s whole-word rule) classify
code points with Go 1.24's Unicode 15.0 tables. Rust's `char` methods and
most crates follow newer Unicode releases, and `is_alphabetic` is the
Alphabetic property, not general category L. Use general categories (L*,
Nd, M*), and add a test listing code points whose category changed after
15.0 if you ever see a fixture drift.

**Invalid UTF-8.** Go strings can hold invalid bytes; the spec says
distances decode each invalid byte to U+FFFD and tokenisers keep invalid
bytes as their own tokens. A Rust `&str` cannot hold them, so the natural
Rust API cannot express those Go tests (`segment_keeps_invalid_utf8_bytes`,
the invalid-byte cases of `tokenisers_are_lossless`). The service inputs
come from protobuf strings, which are valid UTF-8, so take `&str` and skip
those cases, noting it in the test file. If you need byte input later,
decode one U+FFFD per invalid byte (Go's `range` rule), which is not what
`String::from_utf8_lossy` does (it replaces each maximal invalid
subsequence once).

**Map iteration.** `NewSegmenter` iterates the frequency map to validate
and sum; only the first error's message depends on order, not its kind.
`damerau_levenshtein`'s `da` map and patience's counts are lookups only.

Cross-checking against Go:

1. **Fixtures.** `tests/conformance.rs` runs every file in
   `testdata/conformance/dp/` by `kind`, comparing JSON values (key order
   free, unknown keys an error).
2. **Oracles.** Port each Go oracle test with its brute-force oracle
   (all alignments, all splits, all windows, exhaustive plans). They catch
   wrong answers; the fixtures catch wrong tie-breaks.
3. **Differential runs.** For the diff and the planner, generate random
   inputs in Go (a small `go run` program under `tools/` that writes JSONL
   cases with expected outputs, seeded and committed under
   `testdata/crosscheck/dp/`) and replay them in Rust. This catches
   tie-break drift the hand-written fixtures do not cover, especially for
   `best-found` plans.
4. **Fuzzing.** `FuzzDiff` and `FuzzEditDistance` check properties, so a
   Rust port of them finds Rust bugs alone. Copy the `f.Add` seeds from
   `fuzz_test.go` into the Rust corpus, and after a `make fuzz` session
   also any inputs Go saved under `libs/dp/testdata/fuzz/`.
