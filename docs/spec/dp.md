# Spec: dynamic programming library, version 1

This is the normative description of the results `libs/dp` returns. Most of
these problems have several equally good answers; this document fixes which
one is returned, so the Go implementation and the Rust port agree exactly.
Algorithms, recurrences and complexity are documented on each function in
`libs/dp`; what follows is only what an independent implementation must
match. Where this document and the code disagree, one of them has a bug.

## 1. Text

- Positions, lengths and distances count Unicode code points (ADR-0003).
- Edit distances decode invalid UTF-8 bytes to U+FFFD.
- Tokenisers and segmentation slice the original bytes, so concatenating
  their output reproduces the input exactly, invalid bytes included. An
  invalid byte is a token on its own.
- `Lines`: each token ends with `\n`, except possibly the last. `\r` is part
  of the line.
- `Words`: maximal runs of word code points (Unicode letters, digits, marks,
  `_`), maximal runs of white space, and every other code point alone.
- `Chars`: one token per code point.
- Strings (note IDs) compare by bytes, which for UTF-8 is code point order.

## 2. Edit distances

- `Levenshtein`: unit-cost insert, delete, substitute.
- `LevenshteinWithin(a, b, k)`: `(d, true)` when `d <= k`, else
  `(k + 1, false)`. Negative `k` is `0`.
- `DamerauLevenshtein`: the **unrestricted** distance (Lowrance–Wagner): unit
  cost insert, delete, substitute and adjacent transposition, with no
  restriction on editing a substring more than once. It is a metric. It is
  not the optimal string alignment distance: `("ca", "abc")` is `2`.

## 3. Longest common subsequence

`LCS(a, b)` is Hirschberg's algorithm with these choices:

- empty `a` or `b`: empty result;
- `len(a) == 1`: `a[0]` if it occurs in `b`, else empty;
- otherwise split `a` at `mid = len(a) / 2` (integer division), compute
  `F[j] = LCS length of a[:mid] and b[:j]` and
  `B[j] = LCS length of a[mid:] and b[j:]`, split `b` at the **smallest** `j`
  maximising `F[j] + B[j]`, and concatenate the results for
  `(a[:mid], b[:j])` and `(a[mid:], b[j:])`.

## 4. Diffs

A diff is a list of hunks `(kind, elements)` with kind `equal` (1), `insert`
(2) or `delete` (3), the values of `DiffHunk.Kind` in `documents.proto`.

**Canonical form** (both algorithms): no hunk is empty; no two adjacent hunks
share a kind; between two equal hunks (or an end), deleted elements form one
hunk which precedes the one insert hunk.

**Myers** (`Diff`): the shortest edit script, computed as follows; the
matching it produces is part of the spec.

1. Emit the common prefix as equal; strip the common suffix and emit it as
   equal at the end.
2. If one side is empty, the other is all insert or all delete.
3. Otherwise find the middle snake with the linear-space search of Myers
   (1986), section 4b: for `d = 0, 1, ...` run the forward pass for
   `k = -d, -d+2, ..., d`, then the backward pass for the same `k`. A step
   goes down (from `k+1`) when `k == -d`, or when `k != d` and
   `V[k-1] < V[k+1]`; otherwise right (from `k-1`). With `delta = N - M`
   odd, overlap is tested after each forward step on reversed diagonal
   `delta - k`, in range `[-(d-1), d-1]`, as `x_f + x_b >= N`; with `delta`
   even, after each backward step on forward diagonal `delta - k`, in range
   `[-d, d]`. The snake returned is the one just extended (forward: from its
   start to its end; backward: mapped to forward coordinates).
4. Recurse on the parts before and after the snake; the snake is equal.
5. Group single-element operations into canonical hunks.

**Patience** (`PatienceDiff`): strip common prefix and suffix as above; take
the elements occurring exactly once in each side, in order of position in
`a`; find the longest chain increasing in `b` by patience sorting (place each
on the leftmost pile whose top has a larger `b` position; its predecessor is
the top of the pile to the left; reconstruct from the top of the last pile);
recurse between consecutive anchors, emitting each anchor as equal; when a
region has no anchors, use Myers on it.

**Applying** a patch: equal and delete hunks must match the input at the
current position and together consume it exactly.

## 5. Reading plans

Input: notes `(id, minutes, value, prerequisites)` with non-empty unique IDs,
`0 <= minutes <= 2^20`, `0 <= value <= 2^40` (integers), prerequisites that
name given notes (duplicates ignored), and a budget `0 <= budget <= 2^20`.

**Cycles** are rejected. The reported cycle comes from a depth-first search
over "is a prerequisite of" edges, starting from each note in ID order and
following edges in ID order; at the first edge to a note on the current
path, the cycle is that note to the end of the path. It is listed in
prerequisite order and rotated to start at its smallest ID.

**Canonical rank**: a depth-first preorder that starts from the notes without
prerequisites in ID order, visits dependents in ID order, and numbers each
note at its first visit.

**Choice**: among feasible plans (closed under prerequisites, within budget)
the result maximises total value; then minimises total minutes; then, of two
plans, prefers the one containing the lowest-ranked note in their symmetric
difference.

**Reading order**: Kahn's algorithm on the chosen notes, always reading the
ready note of smallest rank.

**`RequiredBy`** of a planned note: the chosen notes listing it as a direct
prerequisite, in reading order.

**Method**: `tree-dp` when every note has at most one prerequisite;
otherwise `branch-and-bound` when the search finishes within the node limit
(default 200,000) and `best-found` when it does not. `tree-dp` and
`branch-and-bound` plans are optimal and fully determined by the rules above.
A `best-found` plan depends on the search order, which the Rust port must
reproduce to match it: the decision order is the reading order of all notes;
a note that cannot be taken (a prerequisite not taken, or not enough time) is
excluded without a node; each node counts one towards the limit, is pruned
when the floored fractional bound is below the incumbent's value (or equal
with more minutes already used), and explores take before exclude; the
incumbent starts as the better of the empty plan and the greedy plan
(`PlanReading` doc comment).

## 6. Chunking

Blocks with token counts `t >= 0` and a limit `1 <= L <= 2^20`. A chunking
splits the blocks into contiguous non-empty chunks of at most `L` tokens.
Cost is the sum over **all** chunks (the last included) of
`(L - tokens)^2`. The result minimises cost; then the number of chunks; then
prefers the lexicographically largest list of chunk end indexes. A block
over `L` is an error; no blocks give no chunks.

## 7. Snippets

Matches `(term, start, end)` with `0 <= start <= end` in code points and a
window `W >= 0`. A span `[s, e)` with `e - s <= W` covers the matches lying
entirely inside it. The result maximises the number of distinct terms
covered; then minimises `e - s`; then minimises `s`. No result when no match
fits. Covered matches are listed by start, then end, then term.

## 8. Word segmentation

From counts `c(w) > 0` (zero counts ignored), with `N = sum c(w)` (or 1 if
none) and `U = max(N, 10)`, costs in integer micro-nats are:

- known word: `round(1e6 · (ln N - ln c(w)))`;
- unknown word of `n` code points:
  `round(1e6 · ln U) + (n - 1) · round(1e6 · ln 10)`.

`round` is round-half-away-from-zero. The result minimises total cost; then
the number of words; then prefers the longest first word (the
lexicographically largest list of word end offsets).

## 9. Conformance fixtures

`testdata/conformance/dp/*.json`, one case per file:

```json
{"name": "...", "why": "...", "kind": "<kind>", "input": {...}, "expect": {...}}
```

| kind | input | expect |
|---|---|---|
| `levenshtein`, `damerau_levenshtein` | `a`, `b` | `distance` |
| `levenshtein_within` | `a`, `b`, `max_distance` | `distance`, `within` |
| `lcs` | `a`, `b` (strings, over code points) | `length`, `lcs` |
| `diff` | `a`, `b`, `tokens` (`lines`, `words`, `chars`), `algorithm` (`myers`, `patience`) | `hunks`: list of `{kind, text}` with kind `equal`, `insert`, `delete` and the tokens joined |
| `reading_plan` | `budget`, `notes` (`id`, `minutes`, `value`, `prerequisites`), optional `node_limit` | `notes` (IDs in reading order), `total_minutes`, `total_value`, `method`, `optimal`; or `error: "cyclic_prerequisites"` with `cycle` |
| `chunk` | `tokens`, `limit` | `ends`, `cost`; or `error: "block_too_large"` |
| `snippet` | `matches` (`term`, `start`, `end`), `window` | `found`, `span` (`start`, `end`), `terms` |
| `segment` | `frequencies` (word to count), `text` | `words`, `cost` |

Results are compared as JSON values, so key order does not matter. Unknown
keys are an error, so a misspelt key cannot silently pass.
