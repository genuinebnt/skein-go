# ML

`MLService` ([`ml.proto`](../../proto/skein/v1/ml.proto)): semantic search,
related-note discovery, topic clusters and clustering benchmarks
([ADR-0010](../adr/0010-search-and-discovery.md), [ADR-0015](../adr/0015-ml-and-agent-in-python.md)).
Served by the Python `ml` service. Results only include notes the caller can
read. Conventions are in [README.md](README.md).

| Method | Path | RPC |
|---|---|---|
| `GET` | `/v1/workspaces/{workspaceId}/semantic-search` | SemanticSearch |
| `GET` | `/v1/notes/{noteId}/related` | Discover |
| `GET` | `/v1/workspaces/{workspaceId}/clusters` | ListClusters |
| `POST` | `/v1/workspaces/{workspaceId}/clusters:benchmark` | BenchmarkClustering |

### SemanticSearch

`GET /v1/workspaces/{workspaceId}/semantic-search`. Blocks nearest to the
query by embedding cosine similarity. `index` picks the vector index
(`INDEX_KIND_BRUTE_FORCE`, `INDEX_KIND_IVF`, `INDEX_KIND_HNSW`); omitted
means the server default. `tags` (repeatable) keeps only notes with all of
them. With `measureRecall=true` the server also runs an exact scan and
reports `recallAtK` of the chosen index against it; otherwise `recallAtK` is
omitted.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/semantic-search?query=how%20do%20followers%20catch%20up&k=2&index=INDEX_KIND_HNSW&measureRecall=true
```

```json skein.v1.SemanticSearchResponse
{
  "hits": [
    {
      "location": {
        "noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
        "blockId": "4831727201934127107:17"
      },
      "cosine": 0.83
    },
    {
      "location": {
        "noteId": "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20",
        "blockId": "9120384756102938475:4"
      },
      "cosine": 0.71
    }
  ],
  "recallAtK": 1,
  "latencyMs": 3.4
}
```

### Discover

`GET /v1/notes/{noteId}/related`. Related notes with three separate
signals, never blended into one score: embedding similarity (`cosine`), tag
overlap (`sharedTags`, `tagJaccard`) and link distance (`hops`, -1 when not
connected). `suggestLink` is true when the meaning is close but the notes
are not linked, which the editor offers as a link suggestion.

```
GET /v1/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f/related?k=2
```

```json skein.v1.DiscoverResponse
{
  "related": [
    {
      "noteId": "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20",
      "cosine": 0.82,
      "sharedTags": 2,
      "tagJaccard": 0.67,
      "hops": 1
    },
    {
      "noteId": "01936f44-6f70-7b81-9ca4-0e1f20314253",
      "cosine": 0.77,
      "hops": -1,
      "suggestLink": true
    }
  ]
}
```

### ListClusters

`GET /v1/workspaces/{workspaceId}/clusters`. The workspace's notes grouped
by topic, from the most recent clustering run, each with a label and its
most characteristic terms.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/clusters
```

```json skein.v1.ListClustersResponse
{
  "clusters": [
    {
      "label": "Consensus",
      "topTerms": ["raft", "leader", "log", "quorum"],
      "noteIds": [
        "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
        "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20",
        "01936f43-5e6f-7a80-8b93-9d0e1f203142"
      ]
    },
    {
      "id": 1,
      "label": "Storage engines",
      "topTerms": ["lsm", "compaction", "bloom"],
      "noteIds": ["01936f44-6f70-7b81-9ca4-0e1f20314253"]
    }
  ]
}
```

The first cluster's `id` is 0 and therefore omitted.

### BenchmarkClustering

`POST /v1/workspaces/{workspaceId}/clusters:benchmark`. Runs clustering
algorithms side by side on the workspace's embeddings, `runs` times each
with `seed`, and reports means. Omitting `algorithms` runs all of them.
`implementation` says whose code produced a row: `scratch` (this repository)
or a reference (`sklearn`, or `faiss` for e-means). Compare algorithms on
`testObjective`, the objective on held-out points. Expensive: it counts
against the agent rate-limit budget.

```json skein.v1.BenchmarkClusteringRequest
{
  "algorithms": ["CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS", "CLUSTERING_ALGORITHM_E_MEANS"],
  "k": 8,
  "runs": 3,
  "seed": "20261116"
}
```

```json skein.v1.BenchmarkClusteringResponse
{
  "results": [
    {
      "algorithm": "CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS",
      "implementation": "scratch",
      "meanSeconds": 0.42,
      "meanIterations": 23,
      "objective": 1834.2,
      "silhouette": 0.31,
      "distanceComputations": "1840000",
      "epochs": 23,
      "testObjective": 471.9
    },
    {
      "algorithm": "CLUSTERING_ALGORITHM_E_MEANS",
      "implementation": "scratch",
      "meanSeconds": 0.07,
      "meanIterations": 160,
      "objective": 1851.6,
      "silhouette": 0.3,
      "distanceComputations": "196000",
      "epochs": 2.1,
      "testObjective": 473.4
    }
  ]
}
```
