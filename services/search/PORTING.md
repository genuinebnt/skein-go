# Porting `services/search` to Rust

Search is a read model. It projects documents' and identity's events into
per-workspace indexes and answers two services from them:

- `SearchService`: typo-tolerant text search over note titles and blocks
  (BM25 ranking), regex code search over code blocks (trigram candidates
  confirmed by the regex), and title suggestions;
- `GraphService`: the readable link graph and its analyses (strongly
  connected components, centrality, PageRank, blast radius, shortest and
  semantic paths, reading paths and reading plans).

Every answer is filtered by what the caller can read, before anything is
read, ranked or counted. Read [docs/porting/README.md](../../docs/porting/README.md),
[platform/PORTING.md](../../platform/PORTING.md) and the library guides for
[trigram](../../libs/trigram/PORTING.md), [dp](../../libs/dp/PORTING.md) and
[graph](../../libs/graph/PORTING.md) first. Slices
[3 (search)](../../docs/porting/slices/03-search.md) and
[4 (the graph)](../../docs/porting/slices/04-graph.md) walk through it end to
end.

## Contract

### RPCs (`proto/skein/v1/search.proto`, `docs/api/search.md`, `docs/api/graph.md`)

| RPC | REST route | Use case (`internal/app`) |
|---|---|---|
| `SearchService.Search` | `GET /v1/workspaces/{workspace_id}/search` | `Searcher.Search` (text or regex mode, filters, cursor pages) |
| `SearchService.SuggestTitles` | `GET /v1/workspaces/{workspace_id}/title-suggestions` | `Searcher.SuggestTitles` |
| `GraphService.GetLinkGraph` | `GET /v1/workspaces/{workspace_id}/graph` | `Grapher.GetLinkGraph` |
| `GraphService.AnalyzeGraph` | `GET /v1/workspaces/{workspace_id}/graph/analysis` | `Grapher.AnalyzeGraph` through `Analyzer` (slow metrics computed in the background) |
| `GraphService.BlastRadius` | `GET /v1/notes/{note_id}/blast-radius` | `Grapher.BlastRadius` |
| `GraphService.ShortestPath` | `GET /v1/notes/{from}/shortest-path/{to}` | `Grapher.ShortestPath` (link hops, or semantic with ml cosines) |
| `GraphService.ReadingPath` | `GET /v1/notes/{note_id}/reading-path` | `Grapher.ReadingPath` |
| `GraphService.ReadingPlan` | `GET /v1/workspaces/{workspace_id}/reading-plan` | `Grapher.ReadingPlan` (budgeted, prerequisites first; `libs/dp`) |

### Events

| Direction | Subscription | Messages |
|---|---|---|
| consumes | `search.documents-events` on `documents.events` | `NoteCreated`, `NoteRenamed`, `NoteTrashed`, `NoteRestored`, `NotePurged`, `BlocksChanged`, `LinksChanged` |
| consumes | `search.identity-events` on `identity.events` | `AccessChanged` (drops cached readable sets and pages) |

Search publishes nothing.

### Calls to other services

Identity `ListReadableNotes` (cached 30 s, failing closed when identity is
unreachable); ml `Discover` and `SemanticSearch`, behind search's own
`Similarity.NoteCosines` and `Relevance.TopicRelevance` ports, for semantic
paths and reading-plan relevance (`ML_ADDR` empty disables them). ml has no
pairwise-similarity RPC, so `NoteCosines` keeps the cosines it finds among
each note's nearest neighbours (a contract follow-up noted in
`adapters/clients/clients.go`).

### Storage

Schema `search`, `migrations/0001_projection.sql`: `notes` and `blocks`
(note states), `links`, `changes` and `feed_state` (the change feed every
replica follows), `snapshots` (encoded in-memory projections, so a restart
does not rebuild from scratch), `projection_positions`,
`processed_events`. Redis: readable sets (30 s), result pages (20 s), both
tag-invalidated.

### Environment

`GRPC_ADDR`, `ADMIN_ADDR`, `IDENTITY_ADDR`, `IDENTITY_JWKS_URL`, `ML_ADDR`,
`GRPC_CLIENT_INSECURE`, `FEED_RETENTION`, plus the platform's (doc comment
of `cmd/search/main.go`). Unlike the other services, search's variable
names have no service prefix; a Rust port must read the same names.

## Learn first

| Topic | Read | What to take from it |
|---|---|---|
| trigram regex search | [Regular Expression Matching with a Trigram Index (Russ Cox)](https://swtch.com/~rsc/regexp/regexp4.html) | how a regex becomes a boolean query over trigrams, which `libs/trigram` implements and search runs per workspace |
| BM25 | [Okapi BM25](https://en.wikipedia.org/wiki/Okapi_BM25) | the ranking formula, `k1`, `b`, field length normalisation; search weights titles `TitleWeight` times |
| roaring bitmaps | [roaringbitmap.org](https://roaringbitmap.org/), [roaring crate](https://docs.rs/roaring/latest/roaring/) | the posting-list format; `libs/trigram` serialises in the portable format, which the crate reads |
| Rust regex | [regex](https://docs.rs/regex/latest/regex/), [regex-syntax](https://docs.rs/regex-syntax/latest/regex_syntax/) | both Go's `regexp` and Rust's `regex` are RE2-style (no backreferences, linear time); `regex-syntax` gives the parsed HIR that the trigram query planner walks |
| graph algorithms | `docs/spec/graph.md`, the graph library guide | which algorithm answers which RPC, and the tie-break rules the truth depends on |
| projections | ADR-0006, ADR-0016 | a projection tracks the last event it applied and is rebuildable from the event stream |
| idempotent consumers | [Idempotent consumer](https://microservices.io/patterns/communication-style/idempotent-consumer.html) | why `Store.Apply` records the event ID in the same transaction as the effect |
| shared state in async | [Tokio tutorial: shared state](https://tokio.rs/tokio/tutorial/shared-state) | when a `std::sync` lock is right in async code (the projection is read-mostly and reads do no I/O while holding it) |

## Port order

Domain, app, ports and fakes go into `search-core`; adapters and `cmd/`
into `search`.

### 1. Domain (`internal/domain`, pure)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `errors.go` | sentinel errors | | |
| `note.go` | `Block` (ordinal, kind, code language), `NoteState` (title, blocks, links, trashed, purged tombstone), `Mutation` and `MutationKind`, `ApplyMutation` (pure, idempotent), `NormalizeLinks` | | `TestApplyMutationIsIdempotent`, `TestBlocksKeepTheirOrdinalAndNewBlocksGoLast`, `TestPurgedNoteIgnoresEveryLaterEvent`, `TestApplyMutationRejectsMalformedMutations` |
| `tokens.go` | `Tokenize` (Unicode words, case folding, CJK handling), `ParseTextQuery`, `TypoBudget` | | `TestTokenizeMatchesTheSeedTruthTokenizer` |
| `wordindex.go` | `WordIndex`: postings and field lengths per note for BM25, vocabulary trigrams for typo candidates; invariants W1 to W3 | tokens, `skein-trigram`, `skein-dp` | the text search tests below |
| `workspace.go` | `Workspace`: block trigram index, title index, word index, the link graph; `put`, `remove`, `index`, `unindex`; invariants S1 to S5 | wordindex, `skein-trigram`, `skein-graph` | `TestProjectionInvariantsHoldAfterEveryChange` |
| `projection.go` | `Projection`: every workspace, applied from note states by difference; invariant P1 | workspace | `TestProjectionInvariantsHoldAfterEveryChange` |
| `textsearch.go` | `SearchText`: every term in title or an allowed block, exact or within the typo budget (banded Levenshtein then Damerau, `libs/dp`), BM25, title and one-block boosts; `RenderText` snippets | workspace, dp | `TestTextSearchFindsExactlyTheNotesHoldingEveryTermExactly`, `TestTextSearchToleratesTyposAndRanksExactMatchesFirst` |
| `regexsearch.go` | `SearchRegex`: readable blocks as a bitmap, intersected with the pattern's trigram candidates, confirmed by the regex | workspace, trigram | `TestRegexSearchMatchesBruteForceOverReadableBlocks` (brute force as the oracle) |
| `titles.go` | `SuggestTitles`: title-index candidates ranked by Damerau distance to the title's closest prefix | workspace, dp | `TestSuggestTitlesCompletesPrefixesAndToleratesTypos` |
| `snapshot.go` | `EncodeSnapshot`, `DecodeSnapshot`: the whole projection, fixed-width little-endian with minimal varints | projection | `TestSnapshotRoundTripsTheProjection`; then a Go-written snapshot decoded in Rust |

Also `TestLinkGraphHoldsOnlyReadableVisibleNotes` and
`TestLinksResolveByTitleWhenTheGraphIsBuilt` (`workspace.go`).

### 2. Application and ports (`internal/app`)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `ports.go` | `AccessDirectory` (identity), `Similarity` and `Relevance` (ml), `Store` (apply mutations with the processed event, change feed, snapshots, prune), `Change`, `Snapshot` | domain | the fakes implement it |
| `errors.go` | `FieldError`, `NotFoundError`, `IsNotFound` | | |
| `projector.go` | `Projector`: decode events, turn them into mutations, `Store.Apply` once per event; identity events invalidate cache tags | ports, `skein-proto` | `TestDuplicateDeliveryIsAppliedOnce`, `TestUndecodablePayloadIsUnprocessable`, `TestPurgeErasesTheNoteEverywhere` |
| `index.go` | `Index`: the in-memory projection behind a read-write lock; `Bootstrap` from the latest snapshot, `CatchUp` on the change feed, periodic `Snapshot` | projector, domain | `TestRandomStreamsWithDuplicatesAndFailuresConverge`, `TestRestartFromSnapshotReplaysOnlyLaterChangesAndMatchesAFullLoad` |
| `access.go` | `Access.Readable`: the caller's readable notes, cached, failing closed | ports | `TestAccessChangedDropsCachedPermissions`, `TestIdentityUnreachableFailsClosed` |
| `search.go` | `Searcher`: validate, page by cursor (tokens bound to their request), cache pages tagged by every note shown | index, access | `TestSearchNeverReturnsOrCountsUnreadableNotes`, `TestPagesAreCursorBasedAndTokensAreBoundToTheirRequest`, `TestEditedNoteIsNotServedFromAStaleCachedPage` |
| `analysis.go` | `Analyzer`: per readable graph and version; slow metrics of large graphs in a background job | index, `skein-graph` | `TestSlowMetricsOfLargeGraphsComeFromTheBackgroundWithTheirVersion` |
| `graph.go` | `Grapher`: the readable view of a workspace or a note, every graph RPC, semantic graph from ml cosines, reading plan through `libs/dp` | analysis, access | `TestBlastRadiusOfAnUnreadableNoteIsNotFound`, `TestReadingPlanIncludesPrerequisitesFirst` |

### 3. Fakes (`internal/fakes/fakes.go`)

In-memory `Store` (with snapshot count and last event, for the restart
tests), `Directory` (grants), `Similarity` and `Relevance`.

### 4. Adapters

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `migrations/0001_projection.sql` | the schema | `skein-postgres` | applies on the native Postgres |
| `adapters/postgres/store.go` | `Apply` (note state, change feed row and processed event in one transaction), `Changes`, `LoadNotes`, `LoadAll`, snapshots, `PruneFeed` | ports | `TestStoreMatchesTheInMemoryFake`, `TestIndexRestartsFromPostgresWithoutRebuilding` |
| `adapters/pubsub/consumer.go` | both subscriptions to the projector; poison messages dead-lettered | projector, `skein-events` | `TestConsumerAppliesRedeliveriesOnceAndDeadLettersPoison` |
| `adapters/clients/clients.go` | identity and ml tonic clients behind the ports | `skein-grpc` | the e2e suite |
| `adapters/grpc/server.go` | both tonic services; errors with documented details; analysis versions in response headers | app | `TestSearchOverGRPCReturnsTheDocumentedShape`, `TestErrorsCarryTheDocumentedDetails`, `TestAnalyzeGraphReportsItsVersionsInHeaders` |

### 5. Seed loader and main

| Go file | Job | Proven by |
|---|---|---|
| `internal/seed/seed.go`, `policy.go`; `cmd/search-seed/main.go` | load notes, blocks and links from the seed through the store; the seed's permission directory | `TestDirectoryAgreesWithThePermissionMatrix`, `TestTextSearchMeetsTheQrels`, `TestTypoQueriesFindTheIntendedNotes`, `TestRegexSearchReturnsExactlyTheTruthMatches`, `TestTitleSuggestionsCompletePrefixesAndTypos`, `TestGraphMatchesSummarySCCsComponentsAndCycles`, `TestBlastRadiusMatchesTheTruthInBothDirections`, `TestShortestPathsMatchTheTruth` (all against `testdata/seed/small/truth/search` and `truth/graph`) |
| `cmd/search/main.go` | wiring; `Index::run` and `Analyzer::run` as tasks; the consumer; servers; shutdown | the e2e suite |

`internal/seed/bench_test.go` holds benchmarks over the seed; port them to
`criterion` and record both numbers in `docs/PERF.md`.

## Dependencies

- Libraries: `libs/trigram` (index, regex query planning, case folding,
  snippets), `libs/dp` (Levenshtein, Damerau, reading plan, snippets),
  `libs/graph` (every graph algorithm).
- Platform: `config`, `logging`, `telemetry`, `auth`, `redisx`, `postgres`,
  `events`, `outbox` (`ProcessOnce`), `grpcx`, `ratelimit`, `shutdown`.
- Generated code: `skein-proto` (search, events, identity and ml clients).
- Other services: identity, ml (optional).

## Crates

`tonic`, `prost`, `sqlx`, `redis`, `regex`, `regex-syntax`, `roaring`,
`unicode-general-category` (to test letters `L`, digits `Nd` and marks
`M` exactly as Go's `unicode` package does), `tokio::sync::RwLock` or `parking_lot::RwLock`,
`thiserror`, `tracing`; tests: `proptest`, `criterion`.

## Pitfalls

- **Permission filtering first.** Every domain function takes the caller's
  readable set and never looks outside it, not even to count. Keep the
  signatures that force it (the readable set as a required argument).
- **Regex dialects.** Go's `regexp` and Rust's `regex` agree on most syntax
  but not all (flags, Perl classes, Unicode class names, empty-match
  iteration). Fixture `013-regex-matches-follow-gos-findall-rules-for-empty-matches`
  and `010-...-kelvin-sign` in `testdata/conformance/trigram` pin the
  behaviour search depends on; make them pass before trusting regex mode.
- **Tokenizer drift.** `TestTokenizeMatchesTheSeedTruthTokenizer` compares
  with the tokenizer the seed's qrels were made with. The Go tokenizer
  keeps code points where `unicode.IsLetter`, `IsDigit` (category `Nd`
  only) or `IsMark` holds, and lowercases one code point at a time with
  `unicode.ToLower`. Rust's `char::is_alphabetic` and `is_numeric` cover
  wider sets, and `char::to_lowercase` can return several characters (which
  would shift offsets); test general categories explicitly and keep
  one-to-one simple lowercasing. Do not swap in a word-segmentation crate.
- **The lock and the await.** Read paths hold the projection's read lock
  and do no I/O; fetch the readable set before taking the lock. A
  `std::sync::RwLock` is fine for that and faster than Tokio's.
- **Snapshot format.** Fixed-width little-endian with minimal varints, and
  decoders that reject non-minimal encodings. A Go replica and a Rust replica
  read each other's snapshots from the same table.
- **Floating-point order.** BM25 and graph scores must be summed in the
  same order as Go to give the same ranking when scores tie closely; the
  seed truth allows ties to break by ID, not by float noise.
- **Fail closed on identity.** When identity is unreachable, search returns
  an error, never unfiltered results.

## Done when

- every Go test above passes in Rust, including the randomised convergence
  test with its seed logged;
- the seed tests meet the qrels and match the graph truth;
- `make e2e` passes with Rust search, notably
  `TestSearchFindsTextTypedIntoANoteOnceIndexed`,
  `TestRegexCodeSearchMatchesOnlyInsideCodeBlocks`,
  `TestTitleSuggestionsTolerateATypo`,
  `TestBlastRadiusAndShortestPathFollowNewlyTypedLinks`,
  `TestSeedSearchFindsEveryRelevantNote`, `TestSeedGraphMatchesTheTruth`
  and `TestSeedTitleSuggestionsMatchTheTruth`.
