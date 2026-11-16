# search

`SearchService` and `GraphService` (`proto/skein/v1/search.proto`,
[docs/api/search.md](../../docs/api/search.md),
[docs/api/graph.md](../../docs/api/graph.md)): typo-tolerant text search,
regex and code search over a trigram index (`libs/trigram`, ADR-0010), title
completion, and the link graph with its algorithms (`libs/graph`,
`libs/dp`, ADR-0011). Everything is limited to the caller's readable notes.

```
cmd/search          the server: gRPC :9090, HTTP health :8080, consumers, follower
cmd/search-seed     seed loader (docs/seed-data.md loader contract)
internal/domain     projection, indexes, text/regex/title search, readable graph (pure)
internal/app        use cases, ports, the change-feed follower, analysis cache
internal/adapters   grpc (handlers), postgres (store), pubsub (consumers), clients (identity, ml)
internal/fakes      in-memory store, identity directory, ml similarity and relevance
internal/seed       loader, seed-policy directory, evaluation against the seed truth
migrations/         the search schema
```

## How it works

```
documents.events ──▶ Projector ──▶ Store.Apply ─┬─ note state (notes, blocks, links)
identity.events  ──▶ Projector ─┐               ├─ processed_events, projection_positions
                                │               └─ change feed (seq, note) ─┐
                                ▼                                           ▼
                     Redis tag invalidation            every replica: Index.CatchUp
                                                       └▶ in-memory domain.Projection
gRPC ──▶ Access (identity ListReadableNotes, cached) ──▶ Searcher / Grapher ──▶ Projection
```

- **Durable projection.** Each documents event becomes a `domain.Mutation`;
  `Store.Apply` records the event as processed, applies the mutation with the
  pure `domain.ApplyMutation`, writes the note's new state and appends the
  note to the change feed, in one transaction. A redelivery is a no-op.
  Purged notes become tombstones that ignore later events.
- **Why a change feed.** Pub/Sub gives each event to one replica, but every
  replica needs every change in memory. Replicas therefore follow the feed
  (`Index.Run`), loading the current state of each changed note. Feed
  positions come from one counter row, so they are gapless and in commit
  order.
- **In memory, per workspace.** A block trigram index (doc IDs per block), a
  title trigram index, and a word index (vocabulary postings for BM25, with
  its own trigram index over words for typo expansion). A block edit
  re-indexes one block.
- **Permissions first.** Every call starts from identity's readable notes.
  Search intersects them with candidates, as a bitmap of block documents,
  before reading, ranking or counting anything. Graphs are built from
  readable notes only. Unreadable notes are `NOT_FOUND`.

### Search

- **Text** (`SEARCH_MODE_TEXT`). Terms are tokens (letters, digits, marks,
  lowercased) or, for CJK, substrings; a note matches when every term occurs
  in its title or a block. Typos: terms of 4 to 7 code points tolerate one
  Damerau edit, longer terms two. Candidate words come from the
  vocabulary's trigram index, are filtered by banded Levenshtein
  (`dp.LevenshteinWithin`) and confirmed by Damerau distance. Ranking is
  BM25 (k1 1.2, b 0.75) over the readable notes, with the title weighted 3x,
  typo variants discounted 0.5 per edit, and boosts when every term is in
  the title (x2) or in one block (x1.5). A hit is a note, located at its best
  block. `candidates` counts the readable blocks proposed for any term;
  `matched` counts the blocks of matching notes that hold a term. Whole-word
  mode changes nothing: terms always match whole tokens. Case-sensitive mode
  disables typo tolerance.
- **Regex** (`SEARCH_MODE_REGEX`). RE2, with `(?i)` unless case-sensitive;
  the candidate query comes from Russ Cox's method, and the regexp confirms
  each match. Hits are blocks in note-ID then block order, score 1, with
  every match highlighted. `candidates` and `matched` count blocks.
- **Filters.** `codeOnly` and `languages` (code blocks only) apply before
  candidates are counted.
- **Pages** are cursor-based (`{fingerprint, score, key}`); a token replayed
  with other parameters is `INVALID_ARGUMENT`.
- **Titles** (`SuggestTitles`). Every title starting with the prefix
  (distance 0), then titles whose start is within the prefix's typo budget,
  by distance then title. Candidates come from the title trigram index; the
  distance is Damerau against the closest title prefix.

### Graph

`GetLinkGraph`, `AnalyzeGraph`, `BlastRadius` (direction defaults to
`CITED_BY`), `ShortestPath` (hops, or semantic weights `1 - cosine` from ml),
`ReadingPath` and `ReadingPlan` run on the caller's readable graph.
Self-links and repeated links are dropped when projected.

- **AnalyzeGraph.** The fast algorithms (components, orphans, SCCs, a cycle,
  topological order, PageRank, Louvain) are cached per readable graph and
  graph version. Betweenness and diameter cost O(V(V+E)). Up to
  `GRAPH_INLINE_LIMIT` notes (2,000) they are computed in the request;
  beyond that, a background worker computes them once per version. Until it
  finishes, requests get the newest finished result, labelled with its
  version, never a blocked request. Since the response has no version
  fields, the versions travel as response headers: `skein-graph-version`,
  `skein-graph-metrics-version` and `skein-graph-metrics-pending`.
- **ReadingPlan.** Relevance comes from ml's `SemanticSearch` (best block
  cosine per note). When ml is not configured or fails, it falls back to
  per-term BM25 normalized to [0, 1]. The 40 most relevant notes go to
  `dp.PlanReading` with relevance quantised to thousandths (integers, as
  libs/dp requires), and values are converted back to doubles in the
  response. Prerequisites are links between different SCCs, since links
  inside a cycle cannot all be prerequisites. Reading time is words / 200,
  at least one minute.

## Caching

| What | Where | Key | TTL | Invalidated by |
|---|---|---|---|---|
| Readable notes | Redis `readable-notes` | user, workspace | 30 s | `AccessChanged` (user tag, or workspace tag when no user) |
| Search pages | Redis `search-results` | user, workspace version, request fingerprint, page | 20 s | tags: user, workspace, every note shown; dropped when such a note changes or is purged |
| Graph analyses | in process | readable graph (workspace + node set), graph version | until evicted (256 entries) | new versions |

Readable notes are needed by every request and cost identity a policy
evaluation, so caching them removes an RPC from nearly every call; the
event keeps revocations prompt and the TTL bounds a lost event. Search
pages are cheap to recompute but repeated by paging and by the command
palette. They are keyed by the workspace version, a feed position that
replicas agree on, so a page is never served after a change. Tags make sure
a purged note's snippet does not stay in Redis for its TTL. If identity is
unreachable, requests fail; permissions are never guessed.

## Persistence, restart and rebuild

Tables (migrations/0001_projection.sql): `notes`, `blocks`, `links`,
`changes` + `feed_state` (the feed), `snapshots`, `processed_events`,
`projection_positions` (each consumer's last applied event and count).

- **Snapshots.** `Index` encodes the projection (note states, block
  document numbering and the serialized trigram index) with the feed
  position it reflects. It does so every 1,000 changes or 10 minutes and
  keeps two. A purge deletes all snapshots in its own transaction (they hold
  text), and a new one is written on the next tick.
- **Restart.** Decode the newest snapshot, then replay the feed from its
  position. Only the notes changed since are re-read.
- **No snapshot, or the feed pruned past it.** The feed pruner deletes
  entries older than `FEED_RETENTION` that a snapshot covers and records
  `pruned_through`; a follower behind that point gets `ErrFeedGap`. In both
  cases every note is loaded from the tables, with no event replay needed.
- **Full rebuild from events** (schema change, corrupted tables):
  1. stop the service; `TRUNCATE notes, blocks, links, changes, snapshots,
     processed_events, projection_positions`; reset `feed_state` to
     `(1, 0, 0)`;
  2. replay documents' history into `search.documents-events`: seek the
     subscription to a time before the oldest retained message
     (`gcloud pubsub subscriptions seek --time`), or have documents
     republish its notes (a documents follow-up: there is no backfill RPC
     yet). Locally, `search-seed` replays the seed dataset the same way;
  3. start the service: it loads the empty tables and follows the feed as
     the events are applied.

## Running

```
make up-min
DATABASE_URL=postgres://search:search@localhost:5432/skein?sslmode=disable \
REDIS_URL=redis://localhost:6379 PUBSUB_PROJECT=skein-local PUBSUB_EMULATOR_HOST=localhost:8085 \
IDENTITY_ADDR=dns:///localhost:9091 go run ./services/search/cmd/search

go run ./services/search/cmd/search-seed -dir testdata/seed/small            # into Postgres
go run ./services/search/cmd/search-seed -dir testdata/seed/small -dry-run    # in memory
```

See the `cmd/search` package comment for every environment variable. Compose:
`deploy/compose/services/search.yml` (gRPC on 127.0.0.1:9093, health on
127.0.0.1:8083).

## Tests

```
cd services/search && go test -race ./...                       # no Docker, no network
go test -tags integration ./internal/adapters/postgres/          # Postgres via testcontainers
```

- `internal/domain`: random mutation streams with every invariant checked
  after every change; text and regex search against brute-force oracles;
  snapshot round trips; titles; the readable graph.
- `internal/app`: random event streams with redeliveries and failed
  transactions converge to "each event once"; restart from snapshot and
  from tables after a feed gap; purge erasure (index, results, snapshots);
  access invalidation; failing closed; paging; background analysis.
- `internal/seed`: the small seed loaded through the real projector,
  checked against its truth. Every text-query qrel must be returned,
  ranking bars (mean MRR >= 0.95, nDCG@10 >= 0.95, typo MRR >= 0.90),
  exact regex matches, exact title completions, SCCs, components,
  orphans, cycles, blast radius both ways, shortest paths, and the access
  directory against the permission matrix.
- `internal/adapters`: gRPC through grpcx's interceptors (auth, error
  details, headers), the Pub/Sub consumer (duplicates, dead letters,
  metrics), and the Postgres store against the fake (integration).

## Contract notes (follow-ups)

- `ChangedBlock` has no position, so a block keeps the order in which it was
  first seen and new blocks go last. Proposal: add `uint32 position` (or a
  `previous_block_id`).
- No event carries `is_root`. The seed loader applies roots directly.
  Proposal: `NoteCreated.is_root` and a `NoteRootChanged` event.
- `AnalyzeGraphResponse` has no version fields. Proposal: `graph_version`,
  `metrics_version` and `metrics_pending`, replacing the headers.
- `MLService` has no pairwise similarity, so semantic path weights come from
  `Discover` neighbour lists, and pairs outside them weigh 1. Proposal: a
  `NoteSimilarities(pairs)` RPC.
- Identity has no JWKS RPC; the key set is fetched from
  `IDENTITY_JWKS_URL`.
