# Porting `services/ml`: keep it in Python

`ml` is a Python service (ADR-0015): embeddings, semantic search over block
vectors (brute force, IVF, HNSW), discovery signals (cosine, shared tags,
link hops, link suggestions), topic clusters (Lloyd with k-means++, Elkan,
mini-batch, e-means, Gaussian mixtures by EM) and the benchmark harness that
compares every from-scratch NumPy implementation with a reference library.
The service README (`README.md` here) is the detailed reference; the
benchmark results are in `docs/ml-benchmarks.md`.

## Recommendation

**Keep the `ml` service in Python. If you want the algorithms in Rust, port
them as library crates and call them from Python through PyO3, not as a
replacement service.**

Reasons:

1. **The service's value is the comparison.** Each algorithm has a
   from-scratch implementation and a library baseline run side by side
   (scikit-learn, FAISS, hnswlib, `faiss.contrib.e_means`). The baselines
   are Python-first; in Rust you would lose them or have to call Python
   anyway.
2. **ADR-0015 decided it**, and nothing since has changed the reasons:
   ONNX Runtime, NumPy and the reference libraries are mature in Python,
   and the Rust port's scope is the Go code.
3. **The contract is small and stable.** Four RPCs and one subscription.
   Nothing in the other services depends on ml being in a particular
   language, so there is no pressure to move it.
4. **The learning goal is still reachable.** The original design doc lists
   `ann`, `cluster` and `embed` crates; writing HNSW and Elkan in Rust
   teaches exactly what it meant to teach. Exposed to Python with PyO3,
   they slot into the existing benchmark harness as one more row next to
   the NumPy and library versions, which is the fairest possible test.
5. **What Rust would buy** is speed of the scratch implementations (Elkan's
   per-centroid loop and HNSW search are pure Python and slow; the README
   says to compare distance counts, not milliseconds). PyO3 crates get that
   without a second service.

Revisit this if ml becomes a latency bottleneck in production (then a Rust
service using the same crates, with `ort` or `candle` for embeddings, is the
natural step) or if Python is to disappear from the stack.

## Contract (what a port would have to match)

| RPC | REST route | Python |
|---|---|---|
| `SemanticSearch` | `GET /v1/workspaces/{workspace_id}/semantic-search` | `MLApp.semantic_search` (`app/service.py`): brute force below 2,000 blocks, HNSW above, unless an index is named; tag and trash filters during the search; optional recall measured against an exact scan |
| `Discover` | `GET /v1/notes/{note_id}/related` | `MLApp.discover`: cosine, shared tags (Jaccard) and link hops returned separately, never blended; `suggest_link` when cosine >= `SKEIN_ML_SUGGEST_THRESHOLD` and hops >= 2 or not connected |
| `ListClusters` | `GET /v1/workspaces/{workspace_id}/clusters` | `MLApp.list_clusters`: `k = round(sqrt(n/2))`, labels by class-based TF-IDF |
| `BenchmarkClustering` | `POST /v1/workspaces/{workspace_id}/clusters:benchmark` | `MLApp.benchmark_clustering` |

Events: consumes `ml.documents-events` (`BlocksChanged`, `LinksChanged`,
`Note*`), deduplicated on `Event.id`. Search calls `Discover` and
`SemanticSearch` (search's `NoteCosines` and `TopicRelevance` ports), and
the agent calls `SemanticSearch`, `Discover` and `ListClusters` as tools.

Storage: schema `ml` (`migrations/0001_init.sql`: `notes`, `blocks`,
`block_embeddings`, `processed_events`), or in memory; Redis for
content-addressed embeddings and workspace-tagged result caches.

Environment: `SKEIN_ML_ADDRESS`, `SKEIN_ML_ADMIN_ADDRESS`,
`SKEIN_ML_DATABASE_URL`, `SKEIN_ML_REDIS_URL`, `SKEIN_ML_PUBSUB_PROJECT`,
`SKEIN_ML_PUBSUB_SUBSCRIPTION`, `SKEIN_ML_EMBEDDER`,
`SKEIN_ML_HASHING_DIMENSION`, `SKEIN_ML_ONNX_MODEL_DIR`,
`SKEIN_ML_CLUSTER_ALGORITHM`, `SKEIN_ML_SUGGEST_THRESHOLD`,
`SKEIN_ML_WORKERS`, `SKEIN_ML_SHUTDOWN_GRACE_SECONDS`,
`SKEIN_ML_LOG_LEVEL` (`config.py`). Note the `SKEIN_ML_` prefix: unlike the
Go services, ml does not read the platform's `DATABASE_URL` and `REDIS_URL`.

### Two gaps to fix before (or while) porting

- **No permission filtering.** ADR-0010 says ml applies permission filters
  during the HNSW descent, but the service does not call identity and does
  not check the caller: any authenticated user who knows a workspace ID gets
  its semantic search results, clusters and related notes. Search avoids
  the problem for the paths it serves by filtering ml's answers with its
  own readable set; direct REST calls and agent tool calls do not. Fix it in
  Python (ask identity's `ListReadableNotes`, pass the readable set as the
  filter the HNSW descent already supports) whatever you decide about Rust.
- **`ml.identity-events` is unused.** `deploy/compose/pubsub-init.sh`
  creates the subscription, but ml only consumes `documents.events`. It
  becomes useful with the fix above (cache readable sets, drop them on
  `AccessChanged`).

## If you port the algorithms (the recommended Rust work)

### Learn first

| Topic | Read | What to take from it |
|---|---|---|
| HNSW | [Malkov and Yashunin, HNSW (arXiv 1603.09320)](https://arxiv.org/abs/1603.09320), [nmslib/hnswlib](https://github.com/nmslib/hnswlib) | layer assignment, greedy descent, the neighbour selection heuristic; hnswlib is the reference the benchmark compares with |
| HNSW in Rust | [hnsw_rs](https://docs.rs/hnsw_rs/latest/hnsw_rs/), [instant-distance](https://docs.rs/instant-distance/latest/instant_distance/) | two existing implementations to compare against yours |
| k-means++ | [Arthur and Vassilvitskii, k-means++](https://theory.stanford.edu/~sergei/papers/kMeansPP-soda.pdf) | the seeding the service uses (greedy, `2 + ln k` trials) |
| e-means | `docs/research/e-means.md` | the 2026 mini-batch algorithm, its recipe and constants |
| classical ML in Rust | [linfa](https://docs.rs/linfa/latest/linfa/) | k-means, GMM and metrics to use as Rust-side baselines |
| embeddings in Rust | [ort](https://docs.rs/ort/latest/ort/), [candle](https://docs.rs/candle-core/latest/candle_core/) | ONNX Runtime bindings (same model files as `adapters/onnx_embedder.py`) or a pure-Rust runtime |
| Python bindings | [PyO3](https://docs.rs/pyo3/latest/pyo3/) | exposing a Rust crate as a Python module (`maturin` builds the wheel) |

### Order

Port the pure `domain/` modules, each with its `check_invariants` and its
oracle test (the README's algorithm table lists both):

1. `domain/search/common.py`, `brute_force.py`: unit vectors, exact top-k.
   Oracle: a full sort.
2. `domain/clustering/init.py`, `lloyd.py`, `elkan.py`: Elkan must equal
   Lloyd from the same initialisation with fewer distance computations.
3. `domain/search/ivf.py`: exact at `nprobe = nlist`, recall monotone in
   `nprobe`.
4. `domain/search/hnsw.py`: layer and degree invariants, filtered search
   with deletions, recall@10 against brute force.
5. `domain/clustering/minibatch.py`, `e_means.py`, `gmm.py`, `metrics.py`.

Expose each as a PyO3 class with the same method names, add it to
`bench/harness.py` as another implementation, and record its row in
`docs/ml-benchmarks.md`. Random number streams differ between NumPy and
Rust, so compare quality metrics (inertia, recall, log-likelihood) over
several seeds, not exact centroids.

### If you later port the whole service

`skein-ml-core` (the crates above plus `app/`), `skein-ml` (tonic server on
`SKEIN_ML_ADDRESS`, admin HTTP, sqlx store, Redis cache, a Pub/Sub consumer
through `skein-events`), and the same environment variables. Accept it when
`tests/test_grpc.py`-equivalent tests, the seed's `truth/clustering` and
`truth/discovery`, and the e2e journeys that touch ml (the agent's
semantic search tools) pass.

## Pitfalls

- Vectors are L2-normalised `float32` (`domain/search/common.py`), while
  NumPy intermediate results default to `float64`; keep the same precision
  per array in Rust (`f32` storage, `f64` accumulators where NumPy
  promotes), or recall and inertia drift.
- HNSW results depend on insertion order and random levels; test recall,
  not exact neighbour lists.
- The hashing-trick embedder (`domain/embedding.py`) is the default in tests
  and must stay byte-compatible if Rust and Python ever share stored
  embeddings.
