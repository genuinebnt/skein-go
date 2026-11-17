# Slice 3: search (text with typos, regex code search, titles)

A user types into a note; a moment later, a search for the words finds it,
a misspelled search still finds it, a regex search finds matches only inside
code blocks, and title suggestions tolerate a typo. Nothing outside what the
user can read is returned, ranked or counted.

## The path

```
documents (from slice 2)
  actor: Debounce after the first unpublished change
    domain.NextProjection(current blocks, last published) -> ChangedBlocks
    Store.PublishProjection: projections row + outbox(BlocksChanged) in one tx
  outbox relay -> Pub/Sub documents.events (ordering key = note ID)
search
  adapters/pubsub/consumer.go (search.documents-events)
    app.Projector.HandleDocuments: decode -> domain.Mutation
    Store.Apply: note state + change feed row + processed event, one tx
  app.Index.CatchUp (every replica): change feed -> domain.Projection.Apply
    Workspace: block trigram index, title index, WordIndex (BM25), link graph

browser GET /v1/workspaces/{id}/search?query=...&mode=...
gateway: auth, search budget -> SearchService.Search
search
  adapters/grpc/server.go Search -> app.Searcher.Search
    app.Access.Readable: identity ListReadableNotes (cached 30 s, fails closed)
    Index.Read (read lock, no I/O):
      text:  domain.ParseTextQuery -> Workspace.SearchText (exact or typo budget,
             trigram candidates over the vocabulary, dp.LevenshteinWithin,
             dp.DamerauLevenshtein, BM25) -> RenderText (trigram.MakeSnippet)
      regex: trigram.CompileRegexp -> candidates bitmap AND readable blocks -> confirm
      titles: Workspace.SuggestTitles (title trigrams, Damerau to the closest prefix)
    page cursor bound to the request; result page cached 20 s, tagged by notes shown
```

## Contracts and storage

- Protos: `search.proto` (`SearchService`, `SearchRequest`, `SearchMode`,
  filters, `SearchResult`, `SuggestTitlesRequest`), `events.proto`
  (`BlocksChanged`, `ChangedBlock`, the `Note*` events), `identity.proto`
  (`ListReadableNotes`), `common.proto` (`PageRequest`, `PageResponse`,
  `BlockRef`).
- Migration: `services/search/migrations/0001_projection.sql` (`notes`,
  `blocks`, `changes`, `feed_state`, `snapshots`, `projection_positions`,
  `processed_events`).
- Specs: `docs/spec/trigram.md` and `docs/spec/dp.md` (normative for the
  libraries); ADR-0010.

## Files to port, in order

Prerequisites: slices 1 and 2 (documents must publish `BlocksChanged`; Go
documents does too, so this slice can run against Go documents).

Libraries ([trigram](../../../libs/trigram/PORTING.md), [dp](../../../libs/dp/PORTING.md)):

| # | Go file | Used for |
|---|---|---|
| 1 | `libs/dp/edit.go` | `LevenshteinWithin` (banded), `DamerauLevenshtein` |
| 2 | `libs/dp/snippet.go` | `BestSnippet` |
| 3 | `libs/trigram/fold.go` | case folding (`Fold`), the smallest member of each case orbit |
| 4 | `libs/trigram/bitmap.go` | roaring bitmaps in the portable format |
| 5 | `libs/trigram/index.go` | `NewIndex`, `UnmarshalIndex`, posting lists |
| 6 | `libs/trigram/query.go`, `regexp.go` | `CompileRegexp`: a regex to a trigram query |
| 7 | `libs/trigram/search.go` | candidates, `Or`, language filters |
| 8 | `libs/trigram/snippet.go` | `MakeSnippet` |

Search ([services/search/PORTING.md](../../../services/search/PORTING.md)):

| # | Go file | What this slice needs from it |
|---|---|---|
| 9 | `internal/domain/errors.go`, `note.go` | note states, mutations, `ApplyMutation` |
| 10 | `internal/domain/tokens.go` | `Tokenize`, `ParseTextQuery`, `TypoBudget` |
| 11 | `internal/domain/wordindex.go` | the BM25 word index and vocabulary trigrams |
| 12 | `internal/domain/workspace.go` | the per-workspace indexes (skip `LinkGraph` until slice 4) |
| 13 | `internal/domain/projection.go` | `Projection.Apply`, `CheckInvariants` |
| 14 | `internal/domain/textsearch.go`, `regexsearch.go`, `titles.go` | the three searches |
| 15 | `internal/domain/snapshot.go` | optional in this slice (see Stubbed) |
| 16 | `internal/app/ports.go`, `errors.go` | the ports |
| 17 | `internal/app/projector.go` | `HandleDocuments` (and `HandleIdentity` as a no-op until slice 5) |
| 18 | `internal/app/index.go` | `Bootstrap`, `CatchUp`, `Read` |
| 19 | `internal/app/access.go` | `Readable` |
| 20 | `internal/app/search.go` | `Searcher.Search`, `SuggestTitles` |
| 21 | `internal/fakes/fakes.go` | store and directory fakes |
| 22 | `internal/adapters/postgres/store.go` | `Apply`, `Changes`, `LoadNotes`, `LoadAll` |
| 23 | `internal/adapters/pubsub/consumer.go` | the `documents.events` subscription |
| 24 | `internal/adapters/clients/clients.go` | `Identity.ListReadableNotes` |
| 25 | `internal/adapters/grpc/server.go` | `Search`, `SuggestTitles` |
| 26 | `internal/seed/seed.go`, `policy.go`, `cmd/search-seed/main.go` | for the qrels tests |
| 27 | `cmd/search/main.go` | wiring |

Gateway: Go, or the Rust gateway with the two search routes (search budget).

## Tests that prove it

Libraries: every fixture in `testdata/conformance/trigram` (in particular
`010` Kelvin sign, `013` empty regex matches, `015` fuzzy candidates, `017`
roaring format) and the `dp` fixtures `001` to `005`, `019`, `020`.

Search unit: `TestApplyMutationIsIdempotent`,
`TestBlocksKeepTheirOrdinalAndNewBlocksGoLast`,
`TestPurgedNoteIgnoresEveryLaterEvent`,
`TestTokenizeMatchesTheSeedTruthTokenizer`,
`TestProjectionInvariantsHoldAfterEveryChange`,
`TestTextSearchFindsExactlyTheNotesHoldingEveryTermExactly`,
`TestTextSearchToleratesTyposAndRanksExactMatchesFirst`,
`TestRegexSearchMatchesBruteForceOverReadableBlocks`,
`TestSuggestTitlesCompletesPrefixesAndToleratesTypos`,
`TestDuplicateDeliveryIsAppliedOnce`,
`TestRandomStreamsWithDuplicatesAndFailuresConverge`,
`TestSearchNeverReturnsOrCountsUnreadableNotes`,
`TestIdentityUnreachableFailsClosed`,
`TestPagesAreCursorBasedAndTokensAreBoundToTheirRequest`,
`TestSearchOverGRPCReturnsTheDocumentedShape`.

Seed: `TestTextSearchMeetsTheQrels`, `TestTypoQueriesFindTheIntendedNotes`,
`TestRegexSearchReturnsExactlyTheTruthMatches`,
`TestTitleSuggestionsCompletePrefixesAndTypos`.

Integration: `TestStoreMatchesTheInMemoryFake`,
`TestConsumerAppliesRedeliveriesOnceAndDeadLettersPoison`.

End to end (`e2e/search_test.go`, `e2e/seed_test.go`):
`TestSearchFindsTextTypedIntoANoteOnceIndexed`,
`TestRegexCodeSearchMatchesOnlyInsideCodeBlocks`,
`TestTitleSuggestionsTolerateATypo`, `TestSeedSearchFindsEveryRelevantNote`,
`TestSeedTitleSuggestionsMatchTheTruth`.

## Stubbed in this slice

- `GraphService`: `UNIMPLEMENTED` until [slice 4](04-graph.md).
- Snapshots: start each replica with `LoadAll` from Postgres instead of the
  latest snapshot; port `snapshot.go` when restart time matters (and then
  `TestRestartFromSnapshotReplaysOnlyLaterChangesAndMatchesAFullLoad`).
- Redis caches: a no-op readable-set cache and no page cache. The page
  cache's invalidation (`TestEditedNoteIsNotServedFromAStaleCachedPage`)
  comes with the Redis adapter.
- `identity.events`: no-op until slice 5.
- ml: `ML_ADDR` empty.

## Build

```
cd rust
cargo test -p skein-dp -p skein-trigram
cargo test -p search-core
cargo build -p search && cargo test -p search --features integration
```
