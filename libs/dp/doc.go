// Package dp holds the dynamic programming algorithms Skein uses (ADR-0011):
// edit distances for typo-tolerant search, longest common subsequences and
// diffs for note history, a reading plan solver, optimal chunking for
// embeddings, snippet selection for search hits and word segmentation for
// queries.
//
// # Contents
//
//   - Levenshtein (two rows), LevenshteinWithin (banded, stops early past a
//     maximum distance) and DamerauLevenshtein (the unrestricted variant,
//     which is a metric; see its doc comment for why not OSA).
//   - LCSLength and LCS (Hirschberg, linear memory), generic over comparable
//     elements.
//   - Diff (Myers O(ND), linear space) and PatienceDiff, generic over
//     comparable elements, with the Lines, Words and Chars tokenisers,
//     ApplyPatch, InvertPatch and CheckHunks. DiffLines, DiffWords,
//     DiffChars, PatienceDiffLines and ApplyTextPatch work on strings.
//   - PlanReading: notes under a time budget maximising value, with
//     prerequisites (precedence-constrained knapsack). Exact tree DP for
//     forests, exact branch and bound with a node limit otherwise, and a
//     reported fallback when the limit is hit. CheckPlan verifies a plan.
//   - ChunkBlocks: contiguous chunks of at most L tokens minimising squared
//     slack. CheckChunks verifies a chunking.
//   - BestSnippet: the span within a window covering the most distinct query
//     terms, then the fewest code points.
//   - Segmenter: word segmentation by a unigram model (Viterbi).
//
// Each function's doc comment states its recurrence, time and memory.
//
// # Determinism
//
// Every result is fully determined by the input, including which of several
// equally good answers is returned. The tie-breaking rules are written on
// each function and pinned by the fixtures in testdata/conformance/dp, which
// the Rust port must pass unchanged. Nothing depends on map iteration order:
// maps are only used for lookups and counts, and anything ordered comes from
// the input or from sorting by ID. Values that must agree across languages
// are integers (reading plan values, segmentation costs in micro-nats) so
// that sums and ties are exact.
//
// # Text
//
// Text functions work on Unicode code points (Go runes, Rust chars), never
// bytes (ADR-0003): distances count code points, Chars yields one token per
// code point, and Match and Span offsets are code point offsets. The
// tokenisers and Segment slice the original string rather than re-encoding
// runes, so invalid UTF-8 survives a round trip; the edit distances decode
// invalid bytes to U+FFFD. None of this is grapheme-aware.
//
// # Invariants
//
// The package has results rather than long-lived data structures, so the
// invariants are result checkers, exported because services can use them on
// input they did not compute (a diff from a client, say):
//
//   - CheckHunks: canonical hunks (non-empty, alternating kinds, deletes
//     before inserts in each change) that turn a into b.
//   - CheckPlan: a feasible plan in reading order with correct totals,
//     within budget, with Method, Optimal and UpperBound consistent.
//   - CheckChunks: contiguous chunks under the limit with the right cost.
//   - Segmenter.CheckInvariants: known word costs lie between zero and the
//     cheapest unknown word.
//
// The tests call them on every result and compare every algorithm with a
// brute-force oracle on small random inputs.
//
// # Porting notes
//
// Standard library only, no I/O, no goroutines, no reflection. Generic
// functions map to Rust generics over `T: Eq` (`T: Eq + Hash` where a map is
// used: DamerauLevenshtein, PatienceDiff). Hunk becomes
// `struct Hunk<T> { kind: HunkKind, elems: Vec<T> }`; with `T = &str` the
// tokenisers can borrow from the input. Lengths and indexes are `usize`;
// costs and values are `i64`. The branch-and-bound recursion is at most one
// level per note; the Go code relies on goroutine stacks growing, so a Rust
// port should either keep plans to a few thousand notes or make the search
// iterative. Go's math.Log and Rust's f64::ln may differ in the last bit;
// segmentation rounds each word cost to an integer, so this matters only if
// a cost lands within 1e-10 of a rounding boundary.
package dp
