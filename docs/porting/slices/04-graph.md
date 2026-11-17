# Slice 4: the graph (links, backlinks, blast radius, paths)

A user types `[[Log replication]]` into a note. The link reaches search's
graph projection, and the graph RPCs follow it: the readable link graph
with backlinks, its analysis (strongly connected components, centrality,
PageRank, communities), the blast radius of a note in either direction
("what cites this", "what this cites"), shortest and semantic paths, reading
paths, and a budgeted reading plan with prerequisites first.

## The path

```
documents
  actor: on debounce, domain.NextProjection(...).Links = blocks.Links(blocks)
    (libs/blocks/extract.go: the [[...]] titles outside code, as typed)
  outbox: LinksChanged{note_id, targets (complete set)}
search
  Projector.HandleDocuments -> Mutation{links} -> Store.Apply (links table, change feed)
  Index.CatchUp -> Workspace: domain.NormalizeLinks; links resolve to notes by exact title
  Workspace.LinkGraph(readable) -> graph.New(nodes, edges)

browser GET /v1/notes/{id}/blast-radius?direction=CITED_BY&depth=2
gateway -> GraphService.BlastRadius
search
  app.Grapher.BlastRadius
    Access.Readable (identity) -> noteView: the readable graph around the note
    graph.BlastRadius(node, direction, depth) -> rings
  other RPCs:
    GetLinkGraph   -> Workspace.LinkGraph (readable, visible notes only)
    AnalyzeGraph   -> app.Analyzer: StronglyConnected, PageRank, Betweenness,
                      Louvain, Modularity, Diameter (slow ones in the background)
    ShortestPath   -> graph.ShortestPath (hops) or graph.ShortestWeightedPath with
                      graph.SemanticDistance over ml cosines (PathMetric)
    ReadingPath    -> graph.ReadingPath
    ReadingPlan    -> dp.PlanReading (budget in minutes, prerequisites first)
```

## Contracts and storage

- Protos: `search.proto` (`GraphService` and its messages,
  `BlastDirection`, `PathMetric`), `events.proto` (`LinksChanged`).
- Migration: search's `0001_projection.sql` (`links`, plus the slice 3
  tables).
- Specs: `docs/spec/graph.md` (normative, including the Louvain label rule
  and tie-breaks), `docs/spec/dp.md` (reading plans); `docs/api/graph.md`.

## Files to port, in order

Prerequisites: slice 3 (the search service's projection and consumer).

Libraries ([graph](../../../libs/graph/PORTING.md), [dp](../../../libs/dp/PORTING.md), [blocks](../../../libs/blocks/PORTING.md)):

| # | Go file | Used for |
|---|---|---|
| 1 | `libs/blocks/extract.go` | `Links` (documents side; done in slice 2 if documents is Rust) |
| 2 | `libs/graph/graph.go`, `errors.go`, `invariants.go` | `New` (rejects self links, unknown notes, repeats), node and edge access, `Direction` |
| 3 | `libs/graph/traverse.go` | `BlastRadius`, `Distances`, `ShortestPath` |
| 4 | `libs/graph/components.go`, `cycle.go`, `topo.go` | `StronglyConnected`, `ConnectedComponents`, `OrphanComponents`, `FindCycle`, `TopologicalOrder` |
| 5 | `libs/graph/dijkstra.go` | `SemanticDistance`, `WeightedDistances`, `ShortestWeightedPath` |
| 6 | `libs/graph/centrality.go` | `PageRank` (with `DefaultPageRankOptions`), `Betweenness` |
| 7 | `libs/graph/community.go`, `diameter.go` | `Louvain`, `Modularity`, `Diameter` |
| 8 | `libs/graph/readingpath.go` | `ReadingPath` |
| 9 | `libs/dp/plan.go`, `plan_solve.go` | `PlanReading`, `PlanOptions`, `MaxPlanMinutes` |

Search ([services/search/PORTING.md](../../../services/search/PORTING.md)):

| # | Go file | What this slice needs from it |
|---|---|---|
| 10 | `internal/domain/note.go` | `NormalizeLinks` and links in `ApplyMutation` |
| 11 | `internal/domain/workspace.go` | `LinkGraph` |
| 12 | `internal/app/analysis.go` | `Analyzer`, `GraphKey`, background jobs |
| 13 | `internal/app/graph.go` | `Grapher` and every graph RPC |
| 14 | `internal/adapters/clients/clients.go` | `ML.NoteCosines`, `ML.TopicRelevance` (optional, see Stubbed) |
| 15 | `internal/adapters/grpc/server.go` | the six `GraphService` methods, analysis versions in headers |

## Tests that prove it

Libraries: every fixture in `testdata/conformance/graph` (`001` to `013`:
link rejection, components, blast radius direction, shortest-path ties,
semantic paths, centrality, the bridge, Louvain ties) and the reading-plan
fixtures `011` to `015` in `testdata/conformance/dp`.

Search unit: `TestLinkGraphHoldsOnlyReadableVisibleNotes`,
`TestLinksResolveByTitleWhenTheGraphIsBuilt`,
`TestBlastRadiusOfAnUnreadableNoteIsNotFound`,
`TestSlowMetricsOfLargeGraphsComeFromTheBackgroundWithTheirVersion`,
`TestReadingPlanIncludesPrerequisitesFirst`,
`TestAnalyzeGraphReportsItsVersionsInHeaders`.

Seed (against `testdata/seed/small/truth/graph`):
`TestGraphMatchesSummarySCCsComponentsAndCycles`,
`TestBlastRadiusMatchesTheTruthInBothDirections`,
`TestShortestPathsMatchTheTruth`.

End to end (`e2e/graph_test.go`, `e2e/seed_test.go`):
`TestBlastRadiusAndShortestPathFollowNewlyTypedLinks`,
`TestSeedGraphMatchesTheTruth`.

## Stubbed in this slice

- ml: with `ML_ADDR` empty, semantic paths and topic relevance are off; the
  hop metric and plans without a topic still work.
- The background analyzer: run every metric inline at first (fine for the
  small seed); move betweenness and diameter to the background job when the
  medium seed makes requests slow.
- The analysis cache: compute per request until the analyzer lands.

## Pitfalls

- Keep every search iterative: Rust test threads have 2 MiB of stack, and
  the Go deep-chain tests cap their own stack to match.
- Sum floating-point values in the same order as Go (PageRank pulls from
  in-links in ascending order; modularity sums communities in label order),
  so results agree to the last bit where they can.
- Links resolve by **exact title within the workspace** when the graph is
  built, not when the event arrives; a renamed note changes which links
  resolve.

## Build

```
cd rust
cargo test -p skein-graph -p skein-dp
cargo test -p search-core
```
