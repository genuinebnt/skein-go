// Package trigram is Skein's trigram inverted index over blocks (ADR-0010):
// the candidate-selection engine behind literal, fuzzy and regular
// expression search in the search service, with filters for code blocks and
// languages, case-sensitive and whole-word modes, and snippet windows.
//
// # Model
//
// An Index holds documents (blocks), each a text, a code flag and a
// language, under caller-assigned 32-bit IDs. For every trigram (three
// consecutive code points after case folding) it keeps the set of
// documents containing it, as a compressed Bitmap. Documents can be added,
// updated and removed one at a time; an update touches only the posting
// lists of the trigrams that changed.
//
// Everything works on Unicode code points, never bytes (ADR-0003): trigrams
// are three code points, and every offset in a result counts code points.
//
// # Case folding
//
// The index stores folded trigrams only. Fold maps each code point to the
// smallest member of its simple case-folding orbit, the equivalence RE2's
// (?i) uses, so one index serves case-sensitive and case-insensitive
// queries: both ask for the same folded trigrams, and the confirmation step
// (an exact or folded comparison, or the compiled regexp) decides case.
//
// # Queries
//
// A search runs in two steps. First a Query, a boolean formula over
// trigrams, is evaluated against the posting lists, giving candidates that
// may match; then each candidate's text is read and the real match is
// confirmed. The query must never exclude a document that matches (it is
// sound); how few non-matching documents it lets through is its
// selectivity, which the search service reports as candidates and matched.
//
//   - A literal needs every trigram of itself (TrigramQuery). Literals
//     shorter than three code points have none and read every document.
//   - A regular expression is parsed with regexp/syntax and translated by
//     Russ Cox's method (RegexpQuery; "Regular Expression Matching with a
//     Trigram Index", 2012): exact, prefix and suffix string sets per
//     subexpression, combined through concatenation, alternation and
//     repetition, and turned into AND/OR of trigrams.
//   - A fuzzy term within k edits needs at least |T(term)| - 4k of its
//     distinct trigrams (FuzzyCandidates, the q-gram count filter); callers
//     confirm with an edit distance from libs/dp.
//
// Filters (CodeOnly, Languages, and Within, the caller's permission bitmap)
// are intersected with the candidates before any text is read, so a
// document outside them is never examined or counted.
//
// Matches are leftmost-first and non-overlapping. For regular expressions
// they are exactly Go's FindAllStringIndex; whole-word mode then keeps the
// matches that neither start nor end inside a word (IsWordRune).
//
// # Bitmaps
//
// Bitmap is a roaring bitmap: 16-bit chunks held as sorted arrays (at most
// 4096 values) or 65536-bit bitsets. It is implemented here rather than
// imported so the library has no dependencies and builds for WebAssembly,
// and it serializes in the portable roaring format, which the Rust
// `roaring` crate reads, so index snapshots written by Go open in Rust.
//
// # Invariants
//
// Bitmap.CheckInvariants (B1-B3: ascending keys, no empty containers,
// canonical container kinds) and Index.CheckInvariants (I1-I4: every
// posting list is exactly the documents whose text contains the trigram,
// no posting list is empty, the code and language sets match the
// documents) are called by the tests after every operation.
// Query.CheckInvariants (Q1-Q4) checks the normal form every constructor
// returns. Randomised tests compare literal, regex and fuzzy search with a
// brute-force scan of every document and log their seed (SKEIN_SEED
// replays one); a fuzz target checks that the regex translation is sound.
//
// # Determinism
//
// Results never depend on map iteration order: hits come in ascending
// document ID, query operands are sorted (trigrams by value, sub-queries by
// their String form), and snapshots write documents and trigrams in
// ascending order. The normal form of queries is part of the spec
// (docs/spec/trigram.md), pinned by fixtures in testdata/conformance/trigram,
// because the candidate counts users see depend on it.
//
// # Porting notes
//
// Standard library plus libs/dp, no I/O, no goroutines. Go's regexp and
// Rust's regex are both RE2-style: syntax and leftmost-first semantics
// agree, and regex-syntax exposes the HIR the analysis walks (Literal,
// Class, Repetition, Concat, Alternation, Look for the empty-width
// assertions). unicode.SimpleFold corresponds to regex-syntax's simple case
// folding tables; Unicode versions must match for the fold fixtures to
// pass. Trigram is a u64, Bitmap a struct of Vec<u16> keys and an enum of
// containers, Query an enum (see QueryKind). Bitmap serialization must be
// byte-identical (golden test and fixture). Offsets convert from byte to
// code point indexes as in RegexpMatches; never slice a Rust str at a
// code point index.
package trigram
