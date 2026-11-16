# Spec: block model, version 1

This is the normative description of Skein's block model (ADR-0005): how a
note's text sequence is divided into blocks, the attributes of a block, the
inline Markdown inside a block, and Markdown import and export of whole
notes. The Go implementation is `libs/blocks`; the Rust port (`skein-blocks`)
must follow this document exactly. It builds on
[`crdt-sequence.md`](crdt-sequence.md). Positions count Unicode code points
(ADR-0003).

## 1. Blocks in the sequence

- A note is one sequence. Each block starts with a **marker**: an element
  whose value is U+001E. Wire format v1 has no marker operation, so a marker
  is inserted like any character (`docs/seed-data.md`).
- A block's **ID** is its marker's operation ID, written `replica:counter`
  in decimal (as `BlockRef.block_id`).
- **Layout**: walk the visible elements in document order. A marker starts a
  new block; any other element appends its value to the current block's
  text. Visible elements before the first marker form a **leading block**
  with ID `0:0` (the root). Block text never contains U+001E.
- Enter inserts a marker; Backspace at a block's start deletes its marker,
  so the block's text joins the previous block, whose attributes it takes.

### Element order without an iterator

An implementation whose sequence exposes only its text may recover the
order of all elements (tombstones included) as `libs/blocks.Elements` does:
replay the log's inserts, in log order, into a scratch sequence, dropping
deletes, renumbering each replica's inserts densely from 1 in their original
order (parents renamed accordingly), and replacing the value of the k-th
insert with code point `k` if `k < 0xD800`, else `k + 0x800`. Fugue's order
depends only on IDs, parents and sides, and the renumbering keeps the
replica and the relative order of counters, so the scratch text lists the
elements in document order. At most 1,112,064 inserts can be ordered this
way.

## 2. Layout invariants

- **L1** The blocks' texts, each preceded by U+001E except a leading block,
  concatenate to the sequence's text.
- **L2** Only the first block may be a leading block.
- **L3** Each block has one element ID per code point of its text, and every
  marker and character ID is visible at the position the layout gives it.

## 3. Attributes

Each block has four registers. A register never written has its default.

| Key | Code | Value | Default |
|---|---|---|---|
| type | 1 | block type (table below) | paragraph |
| indent | 2 | integer `0..=128` | 0 |
| checked | 3 | boolean | false |
| language | 4 | UTF-8, at most 64 bytes, no white space, backticks, control characters, U+001E or U+FFFD | empty |

| Type | Code | | Type | Code |
|---|---|---|---|---|
| paragraph | 1 | | todo | 7 |
| heading1 | 2 | | quote | 8 |
| heading2 | 3 | | code | 9 |
| heading3 | 4 | | divider | 10 |
| bulleted | 5 | | table | 11 |
| numbered | 6 | | | |

An **attribute write** is `(block, key, lamport, replica, value)`. It is
valid when `replica != 0`, `lamport >= 1`, `block` is the root or has
non-zero replica and counter, and the value is valid for the key.

- **Winner.** Of two writes to the same `(block, key)`, the larger
  `(lamport, replica)` wins; if both are equal, the larger encoded value
  (section 4, compared as bytes) wins. This is a total order, so replicas
  agree whatever the delivery order.
- **Independence.** Writes do not depend on the sequence: a write for a
  block whose marker is unknown or deleted is kept and applies if the block
  exists.
- **Duplicates.** Applying an identical write again changes nothing.
- **Stamping.** A new write is stamped `lamport = L + 1`, where `L` is the
  largest Lamport timestamp the writer has seen in operations and writes.
  Writes for a block created in the same local change (Markdown import) may
  use the sequence's clock after the inserts, because no earlier write to
  those registers can exist.
- **History.** The attributes at a version are those produced by the writes
  with `lamport <= M`, where `M` is the largest Lamport timestamp of the
  operations included in the version.

Invariants: **A1** stored writes are valid and distinct; **A2** each
register's winner is the maximum of its writes; **A3** the store's clock is
the largest stored timestamp.

## 4. Attribute wire format, version 1

Integers are unsigned LEB128 in their shortest encoding, as in the CRDT
format.

```
batch = 0x53 0x41 version:u8(=1) count:uvarint write{count}
write = key:u8 block.replica:uvarint block.counter:uvarint
        lamport:uvarint replica:uvarint value
value = type: u8 | indent: uvarint | checked: u8 (0 or 1)
      | language: length:uvarint bytes
```

Decoders reject: wrong magic (`"SA"`) or version, a count larger than the
remaining bytes divided by 6, non-minimal varints, an unknown key or type,
a checked byte other than 0 or 1, an indent above 128, a language longer
than 64 bytes or than the input, truncation, trailing bytes, and any write
invalid under section 3.

Golden example (`TestCodecGoldenBytes`): four writes encode as
`53 41 01 04`, then `01 01 01 02 01 03` (type heading2 of block 1:1, lamport
2, replica 1), `04 AC02 05 07 AC02 02 676F` (language "go"), `03 00 00 03 02
01` (checked on the leading block) and `02 01 09 04 01 8001` (indent 128).

## 5. Inline Markdown

`parse(text)` returns a list of nodes covering the text. Each node has a
kind, a span `[start, end)`, a content span `[content_start, content_end)`
inside it, a value and, for emphasis, strong and links, children covering
the content.

| Kind | Source | Content | Value |
|---|---|---|---|
| text | literal text | itself | the text |
| escape | `\` + one ASCII punctuation character | the character | the character |
| code | n backticks, code, n backticks | the code | the code |
| emphasis | `*x*` or `_x_` | x | empty |
| strong | `**x**` or `__x__` | x | empty |
| link | `[label](url)` | label | url |
| wikilink | `[[target]]` | target | target |
| mention | `@handle` | handle | handle |

**Tokenising.** Scan from the start. At position `i`:

1. `\` followed by ASCII punctuation (`` !"#$%&'()*+,-./:;<=>?@[\]^_`{|}~ ``)
   is an escape.
2. A backtick: let `n` be the run of backticks from `i`. If a later maximal
   run of exactly `n` backticks starts at or after `i + n`, the nearest one
   closes a code span. Otherwise the `n` backticks are text.
3. `[[` followed by one or more code points other than `[`, `]` and newline,
   then `]]`, is a wiki link (the link pattern `\[\[([^\[\]\n]+)\]\]`).
4. Otherwise `[`, a label of code points other than `[`, `]` and newline,
   `](`, a URL of code points other than `)`, `(` and white space, and `)`
   is a link. The label is parsed by these rules, without links.
5. `@` not preceded by a letter, digit, mark, `_`, `.`, `@`, `-` or `+`,
   followed by `[A-Za-z0-9_]` and then `[A-Za-z0-9_.-]*`, with trailing `.`
   and `-` given back, is a mention.
6. A run of `*` or of `_` is a delimiter run (below).
7. Anything else is text.

**Delimiter runs.** With `before` and `after` the code points around the run
(the start and end of the parsed text count as white space), and
punctuation meaning Unicode categories P and S:

- left-flanking: `after` is not white space, and either `after` is not
  punctuation or `before` is white space or punctuation;
- right-flanking: the mirror image.
- `*` opens when left-flanking and closes when right-flanking. `_` opens when
  left-flanking and (not right-flanking or `before` is punctuation), and
  closes when right-flanking and (not left-flanking or `after` is
  punctuation).

**Matching** (CommonMark's "process emphasis" without the rule of three).
For each run that can close, in order: while it has characters left, its
opener is the nearest earlier run of the same character that can open, has
characters left and has not been discarded. If there is none, stop. Use two
characters from each (strong) when both have at least two left, otherwise
one (emphasis); the opener gives up its last characters and the closer its
first. Runs of either character strictly between the pair are discarded.
After the loop, a run that cannot open, or has nothing left, is discarded.
Unused characters of every run are text.

**Tree.** Matches nest by construction. Adjacent text nodes are merged.

Invariants: **P1** nodes cover the text, and children their parent's
content, contiguously; **P2** `start <= content_start <= content_end <= end`,
no empty node, no two adjacent text siblings; **P3** each kind's delimiters
are exactly as in the table, and values match the source.

**Plain text** concatenates text, escape and code values, the children of
containers, wiki link targets, and `@` plus each mention's handle.

**Links and mentions of a note** are the distinct wiki link targets
(mention handles) in every block except code blocks, sorted ascending by
UTF-8 bytes.

## 6. Markdown documents

**Export** writes each block, separated by `\n` when both neighbours are
list items (bulleted, numbered, todo) and `\n\n` otherwise, with no final
newline:

| Block | Markdown |
|---|---|
| heading k | `#`×k, space, text |
| bulleted | 2×indent spaces, `- `, text |
| numbered | 2×indent spaces, number, `. `, text |
| todo | 2×indent spaces, `- [ ] ` or `- [x] `, text |
| quote | each line of text as `> ` + line, or `>` for an empty line |
| code | fence + language, newline, text and newline if text is not empty, fence |
| divider | `---` |
| table | text |
| paragraph | `&nbsp;` if empty; `\` + text if the text needs escaping; text |

The fence is `max(3, k + 1)` backticks, where k is the longest code line
consisting only of backticks and trailing spaces. Numbers count list items
at the same indent since the last non-list block, reset by a bulleted or
todo item at that indent and for deeper levels by a shallower item. Paragraph
text **needs escaping** when, read as one line, it is not a plain paragraph
(rule below).

**Import** normalises `\r\n` and lone `\r` to `\n`, replaces U+001E and
invalid UTF-8 with U+FFFD, and reads lines. A line is classified:

1. A line of one or more backslashes followed by `r` is an **escaped
   paragraph** line if `r` alone would not be a plain paragraph line.
   Otherwise:
2. blank (only white space): separates blocks;
3. exactly `&nbsp;`: an empty paragraph;
4. three or more backticks and an info string without backticks: a code
   fence; the language is the info's first word if valid, else empty; lines
   up to one of only backticks (and trailing spaces), at least as many as
   the fence, are the code (to the end if unclosed);
5. three or more of only `-`, only `*` or only `_` (after trimming): divider;
6. 1 to 6 `#` then a space or the end: heading, level `min(k, 3)`, text
   after one space;
7. starting with `>`: a quote; consecutive such lines, each without `>` and
   one optional space, joined by `\n`;
8. starting with `|`: a table; consecutive such lines joined by `\n`;
9. after leading spaces (indent = spaces / 2, at most 128): `- `, `* ` or
   `+ ` with `[ ]`, `[x]` or `[X]` (then a space or the end) is a todo;
   `-`, `*` or `+` then a space or the end is bulleted; 1 to 9 digits, `.`
   or `)`, then a space or the end is numbered;
10. anything else: a plain paragraph line.

A paragraph is a plain or escaped paragraph line (an escaped line loses its
first backslash) followed by any further such lines, joined by a space.

**Lossless subset.** `import(export(blocks))` equals `blocks` when every
block has: a known type; text without U+001E or `\r` and valid UTF-8; indent
only on list items; checked only on todos; a language only on code blocks,
valid as above; newlines only in code, table and quote text; no text on a
divider; table rows that all start with `|`; and bulleted text that does not
start with a check box (`[ ]`, `[x]`, `[X]` then a space or the end). For any
input, `export(import(md))` is a fixed point of `export ∘ import`.

**Source map.** Each code point of exported Markdown comes from a block's
text at an offset, or is markup (separators, markers, fences, `&nbsp;`, the
escaping backslash) belonging to the block it introduces.

## 7. Conformance fixtures

`testdata/conformance/blocks/*.json` each hold `name`, `why`, `kind`,
`input` and `expect`:

| Kind | Input | Expect |
|---|---|---|
| `inline` | `text` | `nodes` (kind, start, end, content_start, content_end, value if non-empty, children if any), `plain`, `links`, `mentions` (the text as one paragraph) |
| `markdown_import` | `markdown` | `blocks` (type, indent, checked, language, text), `export` |
| `markdown_export` | `blocks` | `markdown`, `lossless` |
| `attrs` | `writes` (block `"r:c"`, key name, lamport, replica, value) | `blocks` (attributes by ID, after applying the writes in order and in reverse), `encoded_hex` |
| `layout` | `replica`, `edits` (`insert {index, text}`, `delete {index, count}`, `set_attrs {block index, attrs}` stamped `L + 1`) | `blocks` with `id`, `markdown` |

## 8. Planned extensions

- A marker operation and attribute writes in the CRDT wire format (version
  2), replacing U+001E and the separate attribute batch.
- A restore operation (crdt-sequence.md section 10), so undoing a block
  deletion brings back the same marker and its attributes.
- A move operation for dragging blocks (ADR-0005, phase 3).
