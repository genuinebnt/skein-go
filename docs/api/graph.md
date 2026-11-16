# Graph

`GraphService` ([`search.proto`](../../proto/skein/v1/search.proto)): the
note link graph, graph algorithms and reading plans
([ADR-0011](../adr/0011-graph-and-dp-libraries.md)). The graph is the
caller's readable notes and the `[[links]]` between them: an edge `from` →
`to` means note `from` links to note `to`. Results are deterministic, with
ties broken by note ID. Conventions are in [README.md](README.md).

| Method | Path | RPC |
|---|---|---|
| `GET` | `/v1/workspaces/{workspaceId}/graph` | GetLinkGraph |
| `GET` | `/v1/workspaces/{workspaceId}/graph/analysis` | AnalyzeGraph |
| `GET` | `/v1/notes/{noteId}/blast-radius` | BlastRadius |
| `GET` | `/v1/notes/{from}/shortest-path/{to}` | ShortestPath |
| `GET` | `/v1/notes/{noteId}/reading-path` | ReadingPath |
| `GET` | `/v1/workspaces/{workspaceId}/reading-plan` | ReadingPlan |

The examples use a three-note workspace: *Raft log compaction* (A) links to
*Raft snapshots* (B) and *Consensus overview* (C); B links to C.

```
A = 01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f   Raft log compaction
B = 01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20   Raft snapshots
C = 01936f43-5e6f-7a80-8b93-9d0e1f203142   Consensus overview
A → B, A → C, B → C
```

### GetLinkGraph

`GET /v1/workspaces/{workspaceId}/graph`.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/graph
```

```json skein.v1.GetLinkGraphResponse
{
  "nodes": [
    {"noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f", "title": "Raft log compaction"},
    {"noteId": "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20", "title": "Raft snapshots"},
    {"noteId": "01936f43-5e6f-7a80-8b93-9d0e1f203142", "title": "Consensus overview", "isRoot": true}
  ],
  "edges": [
    {"from": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f", "to": "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20"},
    {"from": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f", "to": "01936f43-5e6f-7a80-8b93-9d0e1f203142"},
    {"from": "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20", "to": "01936f43-5e6f-7a80-8b93-9d0e1f203142"}
  ]
}
```

### AnalyzeGraph

`GET /v1/workspaces/{workspaceId}/graph/analysis`. Every parameter-free
algorithm at once: connected components (ignoring direction) and orphan
components (containing no root), strongly connected components (numbered so
the one holding the smallest note ID is 0), one example cycle, a
topological order with the notes that cannot be placed because of cycles,
betweenness centrality, PageRank, Louvain communities and their modularity,
and the diameter in hops.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/graph/analysis
```

```json skein.v1.AnalyzeGraphResponse
{
  "componentOf": {
    "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f": 0,
    "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20": 0,
    "01936f43-5e6f-7a80-8b93-9d0e1f203142": 0
  },
  "sccOf": {
    "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f": 0,
    "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20": 1,
    "01936f43-5e6f-7a80-8b93-9d0e1f203142": 2
  },
  "sccSizes": [1, 1, 1],
  "topologicalOrder": [
    "01936f43-5e6f-7a80-8b93-9d0e1f203142",
    "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20",
    "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f"
  ],
  "betweenness": {
    "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f": 0,
    "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20": 0,
    "01936f43-5e6f-7a80-8b93-9d0e1f203142": 0
  },
  "pagerank": {
    "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f": 0.198,
    "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20": 0.282,
    "01936f43-5e6f-7a80-8b93-9d0e1f203142": 0.521
  },
  "communityOf": {
    "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f": 0,
    "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20": 0,
    "01936f43-5e6f-7a80-8b93-9d0e1f203142": 0
  },
  "diameter": 1
}
```

The graph is acyclic and connected, so `cycle`, `unplaced`,
`orphanComponents` and `modularity` are at their defaults and omitted. The
topological order puts every note after the notes it links to, so it is a
reading order: C, then B, then A.

### BlastRadius

`GET /v1/notes/{noteId}/blast-radius`. Every note within reach of a note,
with its distance in hops. `direction` chooses which links are followed:

| `direction` | Follows | Answers |
|---|---|---|
| `BLAST_DIRECTION_CITED_BY` (default, also when omitted or `BLAST_DIRECTION_UNSPECIFIED`) | links backwards, to the notes that link here | "If this note changes, which notes are affected?" |
| `BLAST_DIRECTION_CITES` | links forwards, to the notes this one links to | "What does this note depend on?" |

`maxHops` limits the distance; 0 means no limit. `hops` includes the source
at distance 0, and `ringSizes[i]` is the number of notes at distance `i`.

```
GET /v1/notes/01936f43-5e6f-7a80-8b93-9d0e1f203142/blast-radius?maxHops=3
```

Changing C affects B (which links to it) and A (which links to both):

```json skein.v1.BlastRadiusResponse
{
  "hops": {
    "01936f43-5e6f-7a80-8b93-9d0e1f203142": 0,
    "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f": 1,
    "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20": 1
  },
  "ringSizes": [1, 2]
}
```

```
GET /v1/notes/01936f43-5e6f-7a80-8b93-9d0e1f203142/blast-radius?direction=BLAST_DIRECTION_CITES
```

C links to nothing, so only the source is reached:

```json skein.v1.BlastRadiusResponse
{
  "hops": {"01936f43-5e6f-7a80-8b93-9d0e1f203142": 0},
  "ringSizes": [1]
}
```

### ShortestPath

`GET /v1/notes/{from}/shortest-path/{to}`. `metric` is `PATH_METRIC_HOPS`
(default: link hops, ignoring direction) or `PATH_METRIC_SEMANTIC` (sum of
1 − cosine similarity along links, by Dijkstra). No path is `found: false`,
not an error.

```
GET /v1/notes/01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20/shortest-path/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f?metric=PATH_METRIC_SEMANTIC
```

```json skein.v1.ShortestPathResponse
{
  "found": true,
  "path": [
    "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20",
    "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f"
  ],
  "cost": 0.18
}
```

### ReadingPath

`GET /v1/notes/{noteId}/reading-path`. What to read before a note: the
notes it links to, directly or transitively, ordered so that every note
comes after the notes it links to. `depth` is the shortest link distance from
the requested note; `reason` says why each note is included.

```
GET /v1/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f/reading-path
```

A links to both B and C, and B links to C, so C comes first:

```json skein.v1.ReadingPathResponse
{
  "steps": [
    {
      "noteId": "01936f43-5e6f-7a80-8b93-9d0e1f203142",
      "reason": "linked from Raft log compaction and Raft snapshots",
      "depth": 1
    },
    {
      "noteId": "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20",
      "reason": "linked from Raft log compaction",
      "depth": 1
    }
  ]
}
```

### ReadingPlan

`GET /v1/workspaces/{workspaceId}/reading-plan`. The most valuable set of
notes on `topic` that fits in `minutes` of reading time, with every
included note's prerequisites included before it: a knapsack with
dependencies, solved by dynamic programming. `value` is the note's relevance
to the topic.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/reading-plan?topic=consensus%20protocols&minutes=45
```

```json skein.v1.ReadingPlanResponse
{
  "notes": [
    {
      "noteId": "01936f43-5e6f-7a80-8b93-9d0e1f203142",
      "minutes": 12,
      "value": 0.91,
      "reason": "most relevant to the topic"
    },
    {
      "noteId": "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20",
      "minutes": 20,
      "value": 0.74,
      "reason": "relevant; needs Consensus overview first"
    }
  ],
  "totalMinutes": 32,
  "totalValue": 1.65
}
```
