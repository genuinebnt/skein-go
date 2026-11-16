# Spec: trigram index, version 1

The normative description of what `libs/trigram` returns, so the Go
implementation and the Rust port (`skein-trigram`) agree exactly. Algorithms
and complexity are documented in the package (`doc.go` and each function).
Fixtures in `testdata/conformance/trigram` pin every rule below. Where this
document and the code disagree, one of them has a bug.

## 1. Text

- Text is a string of Unicode code points (ADR-0003). Invalid UTF-8 decodes
  to U+FFFD, one per invalid byte, as Go's `range` over a string does.
- All offsets are code point offsets, half-open `[start, end)`.

## 2. Case folding

`Fold(c)` is the smallest code point in the simple case-folding orbit of `c`
(the cycle Go's `unicode.SimpleFold` walks; Rust: `regex-syntax`'s simple
case folding). Code points with no orbit fold to themselves. Examples:
`k`, `K`, U+212A → `K`; `s`, `S`, U+017F → `S`; `ς`, `σ`, `Σ` → `Σ`.
The Unicode version must match between implementations (Go 1.24: Unicode
15.0).

Two code points are equal *case-insensitively* when they fold to the same
code point. This is exactly the equivalence RE2's `(?i)` uses.

## 3. Trigrams

The trigrams of a text are the distinct triples of consecutive folded code
points, as a sorted set. A trigram is encoded as the 64-bit integer
`f0 << 42 | f1 << 21 | f2`; sorting by that integer sorts by code points.
A text of fewer than three code points has no trigrams. Trigrams span line
breaks.

## 4. Index

The index maps document IDs (u32, assigned by the caller) to documents
`(text, code: bool, language: string)` and every trigram to the set of
documents whose text contains it. Languages compare after lowercasing
`A`–`Z` only. The language of a non-code document is ignored.

Invariants (checked by `CheckInvariants`): every posting list is exactly the
set of documents whose trigrams include it; no posting list is empty; the
code set and each language set are exactly the matching code documents.

## 5. Queries

A query is `All`, `None`, `And(trigrams, subs)` or `Or(trigrams, subs)`.

### 5.1 Normal form

- `And`: trigrams ascending and distinct; subs are `Or`s; an `Or` sub that
  is implied by the `And`'s own trigrams (it lists one of them, or has a
  pure-trigram `And` alternative whose trigrams are all among them) is
  dropped; no trigrams and no subs is `All`; no trigrams and one sub is that
  sub.
- `Or`: alternatives are single trigrams (kept as `trigrams`) and `And`s
  (kept as `subs`); an `Or` operand is flattened; an `And` with one trigram
  and no subs is a single trigram. An `And` alternative is dropped when one
  of its trigrams is a single alternative, or when another alternative is a
  pure-trigram `And` whose trigrams are a subset of its own (strictly fewer,
  or the dropped one has subs). One remaining single is `And([t])`; one
  remaining `And` is itself.
- `And(None, x)` is `None`, `And(All, x)` is `x`, `Or(All, x)` is `All`,
  `Or(None, x)` is `x`.
- Subs are sorted by their string form (5.2), duplicates removed.

### 5.2 String form

`+` is `All`, `-` is `None`. A trigram is its three folded code points in
double quotes, with `"` and `\` escaped by `\`, and code points that are not
graphic (general category L, M, N, P or S, or U+0020) written `\u{hex}` in
lowercase hex. An `And` is its trigrams then its subs separated by single
spaces; an `Or` is `(` its trigrams then its subs separated by `|` `)`; an
`And` with more than one element inside an `Or` is parenthesized.

### 5.3 Literals

The query of a literal is `And` of its trigrams (`All` below three code
points).

### 5.4 Regular expressions

A pattern is compiled with `(?i)` prepended unless the search is case
sensitive. The query is computed from the parsed (Perl flags) and simplified
(counted repetitions expanded) expression by Russ Cox's analysis, over
folded strings, with `maxExact = 7`, `maxSet = 20` and classes of more than
100 code points treated as any character. The algorithm in
`regexp.go` (`analyze`, `concat`, `alternate`, `simplify`, `simplifySet`,
`andTrigrams`) is normative, including:

- literals fold every code point; their exact set is the folded string;
- a class of at most 100 code points is the exact set of its folded members;
- `x*` and `x{0,n}` (after simplification) know nothing (`emptyable`,
  prefix and suffix `{""}`, `All`);
- `x+`: the exact set becomes the prefix and suffix sets;
- empty-width assertions (`^ $ \A \z \b \B`) are the empty string;
- prefix sets sort by code points, suffix sets by code points read from
  the end;
- the final query is `simplify(force)` then `addExact` of the root.

Soundness: if the expression matches anywhere in a text, the query is true
on that text's trigram set. The fuzz target and the oracle test check it.

## 6. Search

Candidates are the documents the query selects, intersected with the
filters (code only; languages, which imply code; and the caller's `Within`
set). Only candidates are read, and `candidates` counts them.

**Matches** are leftmost-first and non-overlapping:

- literal: scan code points from the start; at each position compare the
  literal (exactly, or case-insensitively per section 2); on a match record
  it and continue after it, otherwise advance one code point. An empty
  literal matches nothing.
- regular expression: Go's `FindAllStringIndex` (Rust `find_iter`): an empty
  match abutting the previous match is not reported.

**Whole word**: keep a match `[s, e)` unless the code points at `s-1` and
`s` are both word code points, or those at `e-1` and `e` are (for an empty
match, `s-1` and `s`). A word code point is a Unicode letter, digit or mark,
or `_` (the word class of `libs/dp`'s `Words`).

Hits are the documents with at least one match, by ascending ID.

## 7. Fuzzy candidates

For a term and `k` edits (Damerau–Levenshtein, compared folded), the
candidates are the allowed documents holding at least
`|T(term)| - 4k` of the term's distinct trigrams; when that is zero or
less, every allowed document. (A substitution, insertion or deletion
destroys at most three of the term's trigram occurrences; a transposition
four.) Callers confirm with an edit distance.

## 8. Snippets

`MakeSnippet(text, matches, window)`:

1. `dp.BestSnippet(matches, window)` gives the core span; if none fits, the
   core is the empty span at the earliest match (by start, then end); with
   no matches, the empty span at 0.
2. `start = core.start - (window - len(core)) / 2` (integer division),
   clamped to `[0, max(0, n - window)]`; `end = min(n, start + window)`.
3. Highlights: every match overlapping `[start, end)` (an empty match at
   `start..end` inclusive), clipped and made relative, in order of start,
   end, term.
4. Focus: the first match (same order) starting at or after the core's
   start and, when the core came from `BestSnippet`, ending inside it.

## 9. Encodings

All integers little-endian; varints are unsigned LEB128 and must be minimal.

**Bitmap**: the portable roaring format without run containers: `u32`
cookie 12346, `u32` container count, per container `(u16 key, u16
cardinality - 1)`, per container a `u32` byte offset from the start, then
the containers. A container is an array of `u16` when its cardinality is at
most 4096, else a bitset of 1024 `u64`. Keys strictly ascending, arrays
strictly ascending; anything else is rejected.

**Index snapshot**: `"SKTG"`, `u32` version 1, varint document count, per
document in ascending ID `u32 id`, `u8 flags` (bit 0 = code, other bits
zero), varint-length language bytes, varint-length text bytes; varint
posting count, per trigram ascending `u64 trigram` then one bitmap. Every
posting list must be non-empty and name only documents in the snapshot. No
trailing bytes.
