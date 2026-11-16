# Seed data

`tools/seed` generates deterministic, realistic datasets for testing every
part of Skein: the CRDT and sync, text and code search, the graph
algorithms, clustering and semantic discovery, permissions, notifications
and the agent. Every dataset ships with **ground truth**, the answers a
correct implementation must produce, so tests check correctness and not just
that something ran.

The data is built through the real engine. Each note's content is typed
through edit sessions on several `libs/crdt` replicas: typing bursts with
corrected typos, drafts deleted later, Enter and Backspace (block splits and
merges), concurrent edits at the same spot, offline devices, agent
suggestions, and edits the server must refuse. The encoded operation
batches are written out and replayed through a fresh replica before
anything is written. The CRDT engine is exercised, not bypassed.

All people, companies and content are invented. Email domains use the
reserved `.example` TLD.

## Quick start

```
make seed-small                         # regenerate testdata/seed/small (committed)
make seed PROFILE=medium                # bin/seed/medium (git-ignored)
make seed PROFILE=large SEED=7 SEED_OUT=/data/skein-large
go run ./tools/seed -profile stress -seed 1234 -out /tmp/stress
make seed-check                         # the tool's tests, including the medium time budget
```

The output directory must be missing, empty, or a previous dataset (it holds
a `manifest.json`), which is replaced. Generation writes to `<out>.partial`
and swaps it in only on success, so a failed run leaves nothing half-written.

**Determinism.** The same profile and seed give byte-identical output,
whatever the worker count (`-workers` only changes speed). Every random
choice draws from a stream named by purpose and index (`internal/prng`),
workers only read the plan, and output is written in note order. The tests
generate `small` twice (1 and 3 workers) and compare every byte with the
committed copy. Anything that changes the output for an existing seed bumps
`generate.Version`, which the manifest records.

## Profiles

Users, workspaces and notes are exact. Blocks are a target, met within
±15% (tested). Numbers are from seed 1234 on a 2-core cloud VM shared with
other work, which explains the ranges.

| Profile | Workspaces | Users | Notes | Blocks (target / actual) | CRDT ops | Size | Time | Peak memory |
|---|---|---|---|---|---|---|---|---|
| small | 3 | 25 | 300 | 4,000 / 3,923 | 432,002 | 19.5 MB (3.7 MB compressed in git) | 1 s | under 100 MB |
| medium | 10 | 200 | 8,000 | 150,000 / 149,018 | 16.2 M | 635 MB | 38 to 50 s (budget 180 s, tested) | 152 MB |
| large | 50 | 2,000 | 100,000 | 2,000,000 / 1,973,833 | 176.8 M | 6.6 GB (4.8 GB of it ops) | 8.6 to 14 min | 586 MB |
| stress | 4 | 120 | 150,068 | 151,000 / 151,006 | 4.4 M | 489 MB | 27 to 38 s | 0.95 GB |

Every profile contains the hand-written anchor workspace (62 notes). The
other workspaces are generated (`procedural`), except in `stress`. Size is
dominated by the ops files: wire format v1 has one operation per character
and about 27 bytes per insert (two random 64-bit replica IDs as varints), so
`large` uses terser paragraphs (`MaxSentences` 2). Memory stays bounded
because only small per-note summaries are kept while notes stream to disk,
and idle replicas drop their copy of a note.

Other knobs per profile (`internal/generate/profile.go`): sessions per note
(small 3 to 7, medium 3 to 6, large 2 to 4), chats (40, 300, 2,000, 30),
comment threads per note, the size of the permission matrix, how many
blast-radius sources and path pairs are recorded, and block-embedding
sampling (every note, except every 5th note in large and every 50th in stress).

The **stress** profile replaces the generated workspaces with pathological
shapes:

| Workspace | Shape |
|---|---|
| stress-chain | 100,000 notes, each linking only to the next one (deep traversal, a 99,999-hop shortest path) |
| stress-hub | one hub with 49,700 spokes pointing at it, and a 300-note near-clique core (hub included) that forms one SCC |
| stress-shapes | one note of 200,000+ characters edited deep inside; three code blocks of about 2,500 lines each (Rust, Go, SQL); bullets indented 0 to 64 levels; a note with code points from all 17 Unicode planes (plus combining marks, RTL, ZWJ sequences, noncharacters, BOM, C0 and C1 controls); 500 concurrent replicas editing one note (convergence checked by replaying in reverse order); 400 tiny batches on one note |

## Layout

```
manifest.json                 profile, seed, versions, counts, sha256 of every file
users.jsonl                   people (cast_key names the anchor cast)
workspaces.jsonl
memberships.jsonl             roles, joined_at, removed_at
replicas.jsonl                replica ID -> user (devices and agent runs, ADR-0004)
notes.jsonl                   metadata, counts, ops_path, ops/text hashes
blocks.jsonl                  final blocks: ID, type, indent, checked, language, text
links.jsonl                   every [[link]] interval (added_at, removed_at)
tags.jsonl                    note tags (power-law distribution)
acl.jsonl                     per-note role overrides
share_links.jsonl             share links (token and its sha256; active, expiring, expired, revoked)
comments.jsonl                threads anchored to block ranges, with @mentions
notifications.jsonl           the expected inbox (see "Notification rules")
chats.jsonl                   agent transcripts with tool calls, citations, confirmations
batches.jsonl                 one line per delivered op batch, indexing the ops files
rejected_ops.jsonl            batches the server must refuse (base64 wire format)
ops/<NNNN>/<note_id>.ops      each note's delivered batches (NNNN = note index / 1000)
truth/
  crdt/final.jsonl            per note: final text, version vector, op counts
  crdt/conflicts.jsonl        hand-derived concurrent scenarios with expected merges
  graph/*.jsonl               summary, sccs, cycles, components, hubs, blast_radius,
                              shortest_paths, communities, bridges, dangling
  search/*.jsonl              text_queries (qrels), typo_queries, regex_queries, title_prefixes
  discovery/*.jsonl           suggested_links, near_duplicates
  clustering/                 note_topics.jsonl, block_embeddings.jsonl, topics.json
  permissions/matrix.jsonl    (subject, action, resource) -> allow or deny, with the rule
  agent/evals.jsonl           29 agent evaluation tasks over the anchor workspace
```

Every file is always present, even when empty for a profile. JSON Schemas
for every file are in `testdata/seed/schema/` (`<path with / as .>.schema.json`,
shared definitions in `common.schema.json`), and the small dataset is
validated against them in a test.

### Conventions

- IDs of users, workspaces, notes, comments, chats, messages, runs, share
  links and notifications are UUIDv7, created from the entity's timestamp.
- Timestamps are RFC 3339 UTC with milliseconds. The **reference time**
  ("now") is `2026-11-01T00:00:00.000Z` for every profile. Data spans
  March to October 2026. Time-dependent checks (expiry, removal, trash) are
  evaluated at the reference time unless a record carries its own time.
  Services under test must use a clock fixed to it.
- Replica IDs are decimal strings (they exceed 2^53; proto3 JSON does the
  same for uint64). Real replicas are at or above 2^32; replica 1 is the
  replay checker's.
- Block IDs are `replica:counter`, the ID of the block's marker element,
  as in `BlockRef.block_id` (common.proto).
- Optional values are `null`, never omitted. Every record of a file has the
  same keys.
- Positions (`start`, `end`) count Unicode code points, never bytes or UTF-16
  units (ADR-0003).

## Note text and blocks

ADR-0005 gives each note one sequence with a marker element at the start of
every block. Wire format v1 has no marker operation yet (spec section 10),
so **the seed encodes a marker as the character U+001E** (record separator)
inserted like any other:

```
\u001e<block 0 text>\u001e<block 1 text>...
```

Pressing Enter inserts a U+001E; Backspace at a block start deletes one. A
block's ID is its marker's op ID. Block text never contains U+001E, so
splitting the final text at markers gives exactly the blocks in
`blocks.jsonl`, in order (tested by replaying every note). Block
attributes (type, indent, checked, language) travel in `blocks.jsonl` until
markers carry last-writer-wins attributes in a later wire format. When that
lands, regenerate with a new generator version.

Block types: `paragraph`, `heading1` to `heading3`, `bulleted`, `numbered`,
`todo` (with `checked`), `quote`, `code` (with `language`: go, rust,
python, sql, typescript or yaml), `divider`, `table` (the Markdown table
rows as the block's text). Text is inline Markdown; code blocks hold raw
source.

## Ops files

`ops/<NNNN>/<note_id>.ops` holds a note's delivered batches in delivery
order. Each frame is the batch's length as a uvarint, followed by the batch
in wire format v1 (`docs/spec/crdt-sequence.md` section 7). `batches.jsonl`
indexes every frame (`byte_offset`, `byte_length` of the payload, `sha256`)
and says who sent it and when:

| Field | Meaning |
|---|---|
| `seq` | delivery order within the note, from 0 |
| `replica_id`, `user_id`, `agent_run_id` | the sending replica and whose it is |
| `kind` | `type`, `revise` (deletes a draft), `split`, `merge`, `concurrent`, `offline`, `agent`, `link` |
| `created_at` | when the edits were made (for offline batches, long before delivery) |
| `delivered_at` | when the server received them |

Applying the batches in file order to a fresh sequence never meets an op
that is not causally ready, and yields `truth/crdt/final.jsonl`'s text and
version vector. Every batch's author was allowed to edit the note at
`delivered_at` (tested).

`rejected_ops.jsonl` lists batches the server must refuse: `access_revoked`
(an offline device of a member removed before it reconnected) and
`insufficient_role` (a member lowered to viewer or commenter). Each is a
well-formed batch that is causally ready on top of the accepted history
(tested). The only reason to refuse it is permission. Rejected batches are
not in the ops files, so a naive replay still gives the correct text.

## Content

**Anchor workspace** (`tools/seed/internal/corpus/anchor/notes/*.md`): 62
hand-written notes on CRDTs, Raft, async Rust, LSM trees, vector search and
observability, plus personal-style notes (a reading list, meeting notes, a
1:1, two postmortems, a weekly plan, a Kyoto trip in Japanese, a Berlin
offsite in German, a weekly summary in Hindi, an ideas backlog with deep
nesting, a trashed note, an isolated scratchpad, an orphaned pair). They
are real Markdown with headings, lists, to-dos, quotes, tables and code in
Go, Rust, Python, SQL, TypeScript and YAML. One imported note carries a
prompt injection addressed to AI assistants. The eight cast members (owner,
admin, members, a member with viewer overrides, a guest and a removed
member) are fixed, and the evals refer to them by `cast_key`.

**Generated workspaces** draw from 20 topic models
(`internal/corpus/topics.go`), each with a vocabulary, sentence and title
templates, and preferred code languages. The vocabularies are largely
disjoint, so the generating topic is real signal for clustering. About 15%
of notes mix in a second topic, 3% are written in Hindi, Japanese or German,
and some sentences carry emoji, common misspellings (kept, for typo-tolerant
search) or a foreign-language sentence. Code blocks come from 23 templates
across the six languages, filled with identifiers made from topic words. Tags
follow a Zipf distribution. Titles collide exactly for a few notes (never
link targets) and nearly for more (case, plural, a swapped letter, a suffix).

## Ground truth

All truth is computed from the final data (replayed text, final links),
not taken from the plan, and cross-checked against what was planted:
generation fails if they disagree. The tests then recompute each piece with
independent code.

### Graph (`truth/graph/`)

The graph of a workspace has its non-trashed notes as nodes and an edge
A -> B when A's final text contains `[[B's title]]` outside code blocks.
Links resolve by exact title within the workspace; every link either
resolves to exactly one note or is dangling. Generated workspaces plant:

- **SCCs**: cycles with chords (sizes 3, 5, 8, plus 13 and 21 in larger
  workspaces), a clique (5 or 9), a bare 6-cycle, orphan pairs and 4-cycles.
  Every other edge points forward in a hidden order where each planted SCC
  is a consecutive run. So the planted SCCs are *exactly* the non-trivial
  SCCs of the final graph. The generator checks this with Tarjan's
  algorithm, and the test checks it again with a separate recursive Tarjan.
- **A long chain** ("X series, part k"), **hubs** (notes that many link to,
  last in the order), **roots** (`is_root`: the home note and two topic
  indexes), **orphan components** (sizes 1 to 7, no root), **communities**
  (the generating topic; about 85% of links stay inside one), **bridges**
  (links between communities), **dangling links** (`[[... plan]]` titles that
  match no note) and **links that change over time** (typed then deleted;
  see `links.jsonl`).

Files: `summary` (counts per workspace); `sccs` (non-trivial SCCs, members
sorted, numbered by smallest note ID, `planted` label or null); `cycles`
(planted ones, plus a shortest cycle through the smallest member of every
SCC); `components` (weak components, `has_root`, `orphan`, `planted`);
`hubs` (top 10 by in-degree and by out-degree); `blast_radius` (BFS rings
from planted hubs, the chain head, a root, an SCC member and random notes,
**outbound and inbound**, with the hop of every reached note unless more
than 2,000 are reached); `shortest_paths` (`PATH_METRIC_HOPS`, ignoring
direction: distance, one shortest path, the number of shortest paths, and
`unique`; at least one unreachable pair, into an orphan); `communities`;
`bridges`; `dangling`.

### Clustering (`truth/clustering/`)

`note_topics.jsonl` gives each note's generating topic, secondary topic and
mix weight, for ARI and NMI. Anchor notes get a topic from their area tags
or `null`. `block_embeddings.jsonl` exports a 64-dimension vector per block
from **skein-hash-v1**, a deterministic hashing embedder (normative, so
other languages can reproduce it):

1. Lowercase. A token is a maximal run of letters, digits and combining
   marks, except that each Han, Hiragana or Katakana character is its own
   token. Drop one-character ASCII tokens and the stopwords listed in
   `topics.json`.
2. For each distinct token t (in ascending byte order), h = FNV-1a 32-bit
   of t; add `±(1 + ln tf)` to component `h mod 64`, with the sign negative
   when bit 31 of h is set.
3. L2-normalize; round each component to 4 decimals.

It has real topic signal and no semantics beyond shared words. **Real
embeddings come from the ml service**; these exist so clustering and vector
index code can be tested before it does. `topics.json` lists the topics and
their vocabularies.

### Search (`truth/search/`)

**Text queries** are terms with AND semantics. A term matches a note when it
equals one of the note's tokens (the tokenizer above, without stopword
removal), or, for terms in Han, Hiragana or Katakana, when it is a substring
of the lowercased text. `qrels` lists **every** relevant note, found by an
exhaustive scan. Grades: 3 when every term is in the title, 2 when every
term is in one block, 1 otherwise. Trashed notes are never relevant. Each
query has a `subject_user_id` (the workspace owner, or a guest for the
`permission` queries, whose qrels only include notes the guest can read).
Queries are invented **codenames** planted into 1 to 12 notes each (some in
titles), codename plus topic word, topic word pairs, and hand-picked anchor
queries (including Japanese, Hindi and German).

**Typo queries** misspell a single-term query (transposition, deletion,
insertion or substitution). `target_note_ids` are the intended query's
relevant notes and `edit_distance` is the Levenshtein distance in code
points.

**Regex queries** are RE2 (Go `regexp`) patterns, run per block: `(?i)` is
prefixed when `case_sensitive` is false, `code_only` and `languages` restrict
to code blocks, and matches are leftmost-first and non-overlapping, as code
point ranges into the block text. `matches` lists every match in workspace
note order, then block order. Patterns target the code templates (Go
`Close()` methods, Rust `impl Drop`, SQL `count(*)`, Python tests, TypeScript
exports, YAML image tags), planted codename identifiers (`QuilfenShard`),
and text (`INC-1234`, dates).

**Title prefixes**: `expected` lists every non-trashed title in the
workspace that starts with `prefix`, case-insensitively, sorted by title
(capped at 2,000). Typo prefixes also give the intended note and the edit
distance to that title's prefix.

### Discovery (`truth/discovery/`)

`suggested_links` are **twin pairs**: same-topic notes that share two
invented terms in several sentences, are not linked in either direction,
and should be suggested as links. Each line carries the hashed cosine and
each note's rank among the other's neighbours under skein-hash-v1, as a
baseline for real embeddings. `near_duplicates` are copies of a note with one
block dropped, up to three words changed and one sentence added. They carry
the exact Jaccard similarity of word 3-shingles and the hashed cosine.
Nothing links to a near-duplicate.

### Permissions (`truth/permissions/matrix.jsonl`)

Each line is a subject (a user, a user's agent run, or a share link
holder), an action (the `Action` enum), a resource (a note, or the
workspace for workspace actions), `at` (the reference time), the expected
`allow` or `deny`, and the deciding rule. Subjects per workspace: owner,
admins, removed members, guests, override holders, other members (capped),
two outsiders, an agent run, and every share link. Resources: notes with
overrides or links, trashed notes, and a sample. The policy
(`internal/policy`) is:

1. Not a member at time t (never joined, or removed at or before t): no
   access. Overrides of a removed member are void.
2. Owner or admin: editor on every note; overrides are ignored.
3. Member: editor, unless a per-note override created at or before t says
   otherwise.
4. Guest: no access unless an override grants a role.
5. A share link grants its role on its own note while
   `created_at <= t < expires_at` and it is not revoked at or before t. A
   link holder can only read, comment or edit.
6. From `trashed_at`, only owners and admins can read or delete the note.
7. READ needs viewer, COMMENT commenter, EDIT editor; DELETE needs owner or
   admin, or the creator while an editor; SHARE needs editor and at least
   member. Workspace actions: NOTE_CREATE and AGENT_RUN need member,
   MANAGE_MEMBERS admin, MANAGE_SETTINGS owner. An agent run is decided as
   its user (ADR-0009).

### CRDT (`truth/crdt/`)

`final.jsonl`: per note, the final text (markers included), its sha256 and
length in code points, block count, version vector (sorted by replica), op,
insert, delete and tombstone counts, batch count and replica count.
`conflicts.jsonl`: eight hand-designed scenarios in the conformance step
format (spec section 8) plus `{"reject": "C"}`, each with a `why` derived
from the spec before it ran. Cases: same-spot typing, an insert inside a
concurrently deleted word, a split or a merge racing typing, double
deletes, an offline append racing a new block, a refused revoked replica,
and a three-way split, merge and append. `batches` gives the encoded
batches in a valid delivery order for server-level tests. The generator
fails if libs/crdt disagrees with any expectation.

### Agent (`truth/agent/evals.jsonl`)

29 tasks over the anchor workspace: factual questions (some answered from
Japanese or Hindi text, one about a dangling link), graph questions,
non-destructive actions, deletions and sharing that need confirmation, and
refusals (a viewer asking to edit, a guest asking about a note she cannot
read, a removed member, a member asking about a trashed note). One task
summarises the note containing a prompt injection, with `forbidden_tools`.
Fields: `outcome` (`answer`, `act`, `confirm`, `refuse`), `must_include`
and `must_not_include` (answer text), `citations` (note, block and code
point range of an exact quote), `expected_tools`, `forbidden_tools`,
`confirmation` (tool and target notes) and `why`. Tools are the agent
service's own (`services/agent/src/skein_agent/app/tools/catalogue.py`,
listed in `anchor.AgentTools`): `search`, `get_note`, `create_note`,
`apply_edit`, `delete_note`, `create_share_link`, `set_note_role` and
`blast_radius` appear in the evals. Backlinks are `blast_radius` with
`direction: BLAST_DIRECTION_CITED_BY` and `max_hops: 1`; there is no comment
tool, because no contract has a comment RPC. A confirmation names one tool
and every target note; the agent pauses once per call, so a bulk delete is
one approved confirmation per note.
`chats.jsonl` holds one golden transcript per eval (`eval_id`), showing
the expected behaviour, plus generated chats for UI fixtures. Every tool
call in it uses the real tool's arguments: `services/agent` parses each one
with the tool (`tests/test_seed_evals.py`), and the generator's tests check
each golden transcript calls its expected tools, never a forbidden one, and
confirms every target.

## Notification rules

`notifications.jsonl` is the expected inbox:

- **MENTION** to every `@handle` in a comment, except the author, who can
  read the note when the comment is made. Mentioning someone who cannot read
  it notifies nobody (tested both ways).
- **COMMENT** to the note's creator, and to the parent's author for
  replies, unless already notified for that comment, if they can read it.
- **SHARE** to a guest granted a role by an override; **ACCESS_CHANGED** to a
  member whose role on a note is lowered, and to a removed member.
- **AGENT_RUN_FINISHED** to the user an agent run acted for (chats and agent
  edit sessions).

## Loader contract

Service seed loaders come later, one per service, each reading these files
through its own adapters and APIs. Each loader must:

1. **Check the manifest first**: `format_version` it supports, and the
   sha256 of every file it reads (and `ops_sha256` per note). Refuse on
   mismatch.
2. **Load only what the service owns** (ADR-0001), keeping the dataset's IDs
   as they are, so ground truth applies unchanged:
   - identity: users, workspaces, memberships (insert, then remove members
     with `removed_at`), replicas, acl, share_links (store `token_sha256`,
     never the token).
   - documents: notes, then each note's ops file batch by batch through the
     same apply path live sync uses (version-vector checks, op log, outbox),
     never by writing `blocks.jsonl` rows directly. Then comments. Then try
     every `rejected_ops.jsonl` batch through the permission-checked path and
     assert it is refused with the given reason. Trash notes with
     `trashed_at`.
   - search and ml: build projections from documents' events (or, for
     isolated tests, from `blocks.jsonl` and `links.jsonl`), never from
     truth files.
   - notifications: consume the events the other services publish; compare
     the resulting inbox with `notifications.jsonl` (set equality on
     user, kind and source).
   - agent: chats only, for UI fixtures; evals are inputs to the eval
     harness, not data.
3. **Run with a clock fixed at `reference_time`**, so expiry and removal
   checks match the truth.
4. **Be idempotent**: loading twice changes nothing (inserts keyed by the
   dataset's IDs; ops are naturally idempotent).
5. **Never read `truth/`** while loading; truth is only for assertions.
6. **Verify after loading**: per note, the service's text and version
   vector equal `truth/crdt/final.jsonl`.

## Changing the generator

Edit the code, run `make seed-small`, and commit the regenerated
`testdata/seed/small` with the change; the determinism test fails until
both agree. Bump `generate.Version` when output changes for an existing
seed, and `generate.FormatVersion` and the schemas together when a record
shape changes. Anchor notes are plain Markdown: editing one changes its
facts, and generation fails if an eval citation no longer matches exactly one
block.
