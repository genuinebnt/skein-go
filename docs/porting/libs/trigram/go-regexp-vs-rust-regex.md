# Go `regexp` versus Rust `regex` for `skein-trigram`

`libs/trigram` uses Go's RE2 engine twice: `regexp/syntax` parses a pattern
for the trigram analysis (`regexp.go`), and `regexp` confirms matches
(`search.go`). The Rust port uses `regex-syntax` and `regex` (or
`regex-automata`) for the same jobs. Both are RE2-style (linear time,
leftmost-first, no backreferences), but they are not the same language, and
the analysis walks a parse tree whose shape differs. This page lists every
difference that changes a result, with a fix. Spec:
[`docs/spec/trigram.md`](../../../spec/trigram.md) section 5.4 (the analysis
is normative) and section 6 (matching).

Sources: Go [`regexp/syntax`](https://pkg.go.dev/regexp/syntax) (Perl
classes and `\b` are ASCII; `Simplify` removes counted repetitions), Rust
[`regex`](https://docs.rs/regex/=1.8) (Perl classes and `\b` are Unicode by
default, `(?-u:...)` makes them ASCII, `(?i)` uses simple case folding,
`regex::bytes` matches `&[u8]`), and the
[`regex-syntax` HIR kinds](https://docs.rs/regex-syntax/0.8.8/regex_syntax/hir/enum.HirKind.html)
(`Empty`, `Literal` as bytes, `Class`, `Look`, `Repetition`, `Capture`,
`Concat`, `Alternation`). Check each claim marked "verify" against the
crate versions you pin.

## Recommended shape

1. Parse the pattern (after prefixing `(?i)` unless case sensitive) with
   `regex_syntax::ast::parse::ParserBuilder`, `nest_limit(1000)`.
2. Rewrite the AST so it means what Go means (differences 1 and 2 below).
3. Translate the AST to HIR with `regex_syntax::hir::translate::Translator`.
4. Normalise the HIR into Go's shape (differences 3 to 5), then run the
   ported `analyze`, `simplify(true)` and `addExact`.
5. Build the matcher from the **same** rewritten pattern or HIR, so the
   query and the confirmation can never disagree about the language.
   Either print the rewritten AST back to a string (`regex_syntax::ast::print`)
   and compile it with `regex::Regex`, or build from the HIR with
   `regex_automata::meta::Builder::build_from_hir` (verify the method name
   in your version).

Keep steps 2 and 4 in `src/regexp/normalize.rs` with one test per rule.

## Differences that change which texts match

### 1. Perl classes `\d`, `\s`, `\w` (and `\D`, `\S`, `\W`)

| | Go | Rust default |
|---|---|---|
| `\d` | `[0-9]` | `\p{Nd}` (hundreds of code points) |
| `\s` | `[\t\n\f\r ]` | `\p{White_Space}` |
| `\w` | `[0-9A-Za-z_]` | Alphabetic, marks, digits, connector punctuation, join controls |

Effect on matching: `\d` matches Arabic-Indic digits in Rust and not in
Go. Effect on the query: a class over 100 code points is "any character"
(`maxClass`), so Rust's `\d` gives a weaker query than Go's ten-member
exact set. Fixture `008` (`\bINC-\d{4}\b`) fails.

Fix: in the AST, replace each `Ast::ClassPerl` with the ASCII set Go uses.
Outside brackets, wrapping it as `(?-u:\d)` works (an ASCII-only class is
allowed in a Unicode regex). Inside brackets (`[\d_]`, a
`ClassSetItem::Perl`) there is no group to wrap, so replace the item with
explicit ranges. Use Go's sets exactly: `\s` is `[\t\n\f\r ]`, which is
**not** POSIX `[[:space:]]` (that adds `\v`).

### 2. Word boundaries `\b` and `\B`

Go's `\b` is an ASCII word boundary; Rust's is a Unicode one. On `é1`, Go
sees a boundary between `é` and `1`, Rust does not. The query treats both
as the empty string, so only confirmation changes.

Fix: rewrite to `(?-u:\b)` / `(?-u:\B)` (HIR `Look::WordAscii` and
`Look::WordAsciiNegate`). Older `regex` versions refused ASCII word
boundaries in a Unicode `Regex` over `&str`; verify yours accepts it, and
otherwise match with `regex::bytes::Regex` over the text's UTF-8 bytes
(the offsets are the same bytes either way).

Note that the whole-word option (`Options.whole_word`) is a different,
Unicode-aware rule implemented in `search.go` (`IsWordRune`), applied after
matching; it is not `\b`. Port it as written.

### 3. Syntax only one side accepts

Rust accepts things Go rejects, for example the `x` (verbose) flag, class
set operations such as `[a-z&&[^aeiou]]` and `\b{start}`. Go accepts
`\Q...\E` quoting (verify whether your `regex` version does). The service
maps a pattern that does not compile to `INVALID_ARGUMENT`, so a different
accepted set is visible to users. Decide once: either reject Rust-only
syntax in the AST walk (it is easy to detect there) so both languages
accept the same patterns, or document that Rust accepts a superset. The
first keeps the spec's "a pattern that does not parse is invalid" exact.

### 4. Limits

Go rejects nesting deeper than 1000 and counted repetitions above 1000.
`regex-syntax`'s default nest limit is lower (verify: 250 in recent
versions), and its repetition and size limits are different. Set
`nest_limit(1000)`, reject counts above 1000 in the AST walk, and set a
size limit large enough that every pattern Go compiles also compiles in
Rust (the regex query corpus below will find any gap).

### 5. Empty matches in "find all"

Spec section 6: matches are Go's `FindAllStringIndex`, which does not
report an empty match that abuts the previous match. Fixture `013` runs
`a*` on `baa` and expects `[0,0]` and `[1,3]` only. Run Rust's `find_iter`
on it; if it also yields `[3,3]`, drop any empty match whose start equals
the previous match's end.

### 6. Invalid UTF-8

Go matches strings containing invalid bytes, treating each as U+FFFD. Rust
`regex::Regex` takes `&str`, which cannot contain them. Index texts come
from protobuf strings (valid UTF-8), so this matters only if you add a
bytes API; then use `regex::bytes::Regex` and decode one U+FFFD per invalid
byte when converting offsets.

## Differences that change the query but not the matches

These leave the matched language unchanged but change the tree the
analysis walks, and so possibly the query, its string form and the
`candidates` count users see. The spec makes the query normative, so they
matter.

### 7. Counted repetition

Go's analysis runs on `re.Simplify()`, which expands counted repetitions:
`x{2,5}` becomes `xx(x(x(x)?)?)?`, `x{3,}` becomes `xxx+`, `x{0}` the empty
match, `x{1}` just `x` (read `simplify.go` in Go's `regexp/syntax` for the
exact rules). `regex-syntax` keeps `Repetition { min, max, greedy, sub }`.
Port Go's `Simplify` over HIR as a normalisation pass, producing the same
nested `Concat`/`Repetition{0,1}` shape, before `analyze`. Fixture `008`
(`\d{4}`) covers the simplest case.

Map the remaining repetitions as Go's ops: `{0,1}` is `OpQuest`, `{0,}` is
`OpStar`, `{1,}` is `OpPlus`.

### 8. Literals and case folding

- Rust `Literal` holds **bytes** (UTF-8 in Unicode mode). Decode to chars
  before folding.
- Go parses `(?i)Raft` as one `OpLiteral` with the fold-case flag, and
  `analyze` folds it into one exact string `RAFT`. `regex-syntax` turns each
  letter into a class (`[Rr][Aa][Ff][Tt]`), so the Rust tree is a `Concat`
  of four classes.

The exact sets agree (each class folds to one code point), but `simplify`
runs after every `concat` step, and it fires as soon as an exact string
reaches four code points. For long literals the trigram set comes out the
same; the order in which ANDs and ORs are built can differ. Fix: in the
normalisation pass, turn any class whose members all fold to one code
point into a literal of that code point, then merge adjacent literals in a
`Concat`, so the Rust tree has Go's shape. Fixture `004` (`Raft`, case
insensitive) and `010` (the Kelvin sign) check it.

### 9. Alternation factoring

Go's parser factors alternations: common prefixes are pulled out
(`abc|abd` becomes `ab(?:c|d)`), and alternatives that are single
characters merge into a class (`a|b` becomes `[ab]`). `regex-syntax`'s
smart constructors simplify alternations too, but not with the same rules.
The analysis of `ab[cd]` and of `abc|abd` gives the same exact set
`{ABC, ABD}`, so most queries agree, but not every shape is guaranteed to.
Fix: rely on the corpus check below, and only port Go's factoring
(`parse.go`, `factor`) if the corpus shows differences.

### 10. Empty-width assertions and any-character

All Rust `Look` variants (start and end of text or line, word boundaries in
either mode) are Go's `OpBeginLine`, `OpEndLine`, `OpBeginText`,
`OpEndText`, `OpWordBoundary`, `OpNoWordBoundary`: the empty string. Rust's
`.` is a `Class` of every code point but `\n`; it is over 100 code points,
so `classInfo` makes it "any character", matching Go's `OpAnyCharNotNL`.
An empty class (fixture `009`, `[^\x00-\x{10FFFF}]`) is `noMatch` in both.

## Case folding for the index

`Fold(c)` is the smallest code point in `c`'s simple case-folding orbit
(Go's `unicode.SimpleFold` cycle). In Rust, build a `ClassUnicode` holding
only `c`, call `case_fold_simple()`, and take the smallest code point of
the result. That uses the same Unicode tables as the regex engine, which
is what makes the index sound for `(?i)`. Check that the `regex-syntax`
Unicode version matches Go 1.24's 15.0, or at least that fixtures `001`,
`010` and `011` pass.

## Proving it: the regex query corpus

The fixtures cover a dozen shapes. To cover the rest, generate a corpus
from Go once and commit it:

- inputs: the patterns in `libs/trigram` tests and fixtures, the
  benchmark patterns, the regex queries in the small seed's search truth,
  and a few thousand from `randomRegexp` in `helpers_test.go`, each with
  `case_sensitive` true and false;
- output, one JSON line per input: `pattern`, `case_sensitive`, the
  query's spec 5.2 string form, and for a few random texts per pattern the
  match spans.

A Rust test reads `testdata/crosscheck/trigram/regex-queries.jsonl` and
compares both. Every mismatch is either a missing normalisation rule (add
it, with a test) or a deliberate difference (write it on this page).
