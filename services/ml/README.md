# ml service

Embeddings, semantic search, discovery and clustering for Skein
(`skein.v1.MLService`, see `proto/skein/v1/ml.proto`; ADR-0010, ADR-0015).
Every algorithm is implemented from scratch in NumPy and benchmarked against
a reference library; results are in [`docs/ml-benchmarks.md`](../../docs/ml-benchmarks.md).

## Layout

```
src/skein_ml/
  domain/            pure: no I/O, no global state, seeded
    clustering/      k-means++, Lloyd, Elkan, mini-batch, e-means, GMM (EM), silhouette
    search/          brute force, IVF, HNSW, recall@k
    embedding.py     Embedder protocol, hashing-trick TF-IDF embedder
    discovery.py     cosine, shared tags (Jaccard), link hops; link suggestions
    text.py notes.py tokenising, #tags, cluster labels; the note projection
  app/               use cases (MLApp), port interfaces, events, typed errors, per-workspace indexes
  adapters/          grpc_server, events_codec, consumer, events, postgres, redis_cache,
                     onnx_embedder, observability, memory (in-memory ports)
  bench/             benchmark harness, datasets, search benchmark, CLI
  config.py main.py  environment configuration; wiring and graceful shutdown
tests/               pytest; no test needs Docker, the network or another service
migrations/          SQL for the optional Postgres adapter (schema `ml`)
```

Generated messages and stubs come from the shared package in
[`gen/python`](../../gen/python) (`skein_proto.v1`), a path dependency in
`pyproject.toml`; after `make proto` changes it, reinstall the service.

## Quick start

```
cd services/ml
python3 -m venv .venv && . .venv/bin/activate
pip install -e '.[dev,bench,redis,postgres,pubsub]'   # also installs ../../gen/python
pytest                          # unit tests (integration, onnx, loopback are opt-in)
pytest -m loopback              # the real gRPC server on 127.0.0.1
ruff check src tests
python -m skein_ml.bench        # writes docs/ml-benchmarks.md (about 3 minutes on 2 cores)
OTEL_SDK_DISABLED=true python -m skein_ml   # gRPC on :9090, /healthz and /readyz on :8080, in memory
```

Randomised tests print their seed; replay a failure with `SKEIN_SEED=<seed> pytest ...`.

Docker (no daemon was available while writing this, so the image is untested;
the same steps were replayed by hand with a non-editable install):

```
docker build -f services/ml/Dockerfile -t skein/ml .      # from the repository root
```

Compose: [`deploy/compose/services/ml.yml`](../../deploy/compose/services/ml.yml)
publishes gRPC on `127.0.0.1:9105` and `/healthz`, `/readyz` on
`127.0.0.1:8105` (container ports 9090 and 8080), with Postgres (role and schema `ml`), Redis
database 1 and the `ml.documents-events` subscription.

## Algorithms

All implementations count distance computations and expose `check_invariants`;
the tests call it after every iteration through `fit(..., observer=...)`.

| Algorithm | Module | Invariant checked | Oracle in tests |
|---|---|---|---|
| k-means++ (greedy, `2 + ln k` trials) | `clustering/init.py` | distinct data points | seeding quality vs scikit-learn (benchmark) |
| Lloyd, random or k-means++ init | `clustering/lloyd.py` | objective never increases | labels = nearest centroid at convergence |
| Elkan | `clustering/elkan.py` | bounds bracket true distances | equals Lloyd from the same init, fewer distances |
| MacQueen mini-batch | `clustering/minibatch.py` | masses sum to points seen | |
| e-means | `clustering/e_means.py` | masses finite, total matches the decay | one Lloyd step at B = n, alpha = 1; recipe vs formulas and FAISS; centroids and labels bit-exact vs `faiss.contrib.e_means` given the same assignment kernel |
| Gaussian mixture, diagonal, EM | `clustering/gmm.py` | weights a distribution, variances >= reg, log-likelihood never decreases | |
| Silhouette | `clustering/metrics.py` | | scikit-learn `silhouette_score` |
| Brute force cosine | `search/brute_force.py` | rows unit length | full sort |
| IVF-Flat | `search/ivf.py` | each id in its nearest cell's list | exact at nprobe = nlist; recall monotone in nprobe |
| HNSW | `search/hnsw.py` | layers, degree bounds, entry point on top layer | recall@10 against brute force, filtered and with deletions |

**e-means** reimplements the FAISS reference (`faiss/contrib/e_means`,
commit `ebab8f7`, MIT licence, Meta Platforms) of "E-Means: Scaling Laws for
Mini-Batch K-Means" (Bessa, Mazaré, Szilvasy et al., arXiv 2611.09501). Credit
for the algorithm, recipe and constants belongs to its authors. It follows the
reference's RNG usage, so for the same seed it visits the same batches and
produces the same centroids as `NumpyEmeans` (the benchmark rows agree to every
printed digit). The one source of difference is the assignment kernel:
`faiss.knn` and NumPy round `|x|^2 - 2 x.c + |c|^2` differently, so a point
within float32 rounding of two centroids (common right after a split, when
donor and child are 2e-4 apart) can go either way, and that run then takes
another path. The fidelity tests therefore run the reference with our kernel
and require bit-exact agreement, and separately check that every
`faiss.knn` disagreement is such a tie. Notes: [`docs/research/e-means.md`](../../docs/research/e-means.md).

**HNSW** filters during the descent: excluded and deleted nodes are traversed
but never enter the result set, so a 5% filter keeps recall@10 at 1.0 where
filtering after the search drops it to about 0.3 (see the benchmark).

## Reading the benchmark

- Scratch Lloyd and Elkan agree exactly; Elkan needs about 3 to 10 times fewer
  distance computations but is slower in wall time at d = 128, because its
  per-centroid loop runs in Python. The count is the hardware-independent result.
- k-means++ seeding matches scikit-learn's (median potential after seeding
  31.96 vs 31.60 over 30 seeds on the blobs); with 3 seeds per row one bad seed
  (a local minimum, 2 in 10 seeds) moves the mean, which explains the gap to
  scikit-learn on the blobs.
- e-means starts from random points, as the reference does. With 3 epochs and
  an eighth of faiss.Kmeans's distance computations it beats faiss.Kmeans at 25
  iterations on held-out loss on both sets (blobs 58.6 vs 79.1; embeddings-like
  0.491 vs 0.497), and on the embeddings-like set comes within 1% of converged
  Lloyd (0.486 to 0.490, after 79 to 88 passes). On the blobs, k-means++ Lloyd
  (18.0) is far better: random starts leave merged blobs that 3 epochs of
  revival do not undo. The laws were fitted on 1e5 to 1e7 rows, far above
  these sizes.
- e-means seeded by k-means++ (`scratch-kmeans++`, our addition; the
  reference only has random starts) closes that gap. On the blobs it reaches
  converged k-means++ Lloyd's held-out loss exactly (17.963 against 58.64
  with random starts) with half of Lloyd's distance computations (5.0M
  against 10.6M); on the embeddings-like set it gains 0.7% (0.4877 against
  0.4912) and ends 0.3% from converged Lloyd with a tenth of its distances.
  The price is the seeding: about two thirds of its distance computations
  and twice to three times the wall time of random-start e-means, so the
  "fraction of Lloyd's passes" advantage now mostly measures k-means++.
- Scratch search latency is pure Python; compare recall and distances per
  query, not milliseconds, against hnswlib and FAISS.

## Service behaviour

- **Events.** `documents.events` (`BlocksChanged`, `LinksChanged`, `Note*`)
  update the projection, the block embeddings and the in-memory indexes.
  Consumption is idempotent (deduplicated on `Event.id`, recorded only after
  applying); malformed messages are logged and acknowledged; failures are
  redelivered. Tags are the `#hashtags` in note text.
- **Search.** `SemanticSearch` uses brute force below 2,000 blocks and HNSW
  above unless an index is named; tag and trash filters are applied inside the
  search; `measure_recall` runs an exact scan beside it.
- **Discovery.** Cosine, shared tags with Jaccard, and link hops (`-1` = not
  connected) are returned separately, never blended; `suggest_link` is set when
  cosine >= `SKEIN_ML_SUGGEST_THRESHOLD` (0.70) and hops >= 2 or -1.
- **Clusters.** Lloyd with k-means++ on note vectors by default
  (`SKEIN_ML_CLUSTER_ALGORITHM`: `lloyd`, `e_means`, or `e_means_kmeans++`),
  `k = round(sqrt(n/2))`, notes labelled by their nearest final centroid,
  clusters by class-based TF-IDF terms. Workspaces are small, where Lloyd
  converges in milliseconds; e-means pays off on large ones.
- **Admin HTTP.** `/healthz` (process up) and `/readyz` (503 once draining)
  on `SKEIN_ML_ADMIN_ADDRESS` (`:8080`), beside gRPC on `:9090`.
- **Cache.** Embeddings are content-addressed; search results and clusters are
  tagged by workspace and invalidated by every change to it. Redis
  (`SKEIN_ML_REDIS_URL`) or in-memory.
- **Storage.** Postgres (`SKEIN_ML_DATABASE_URL`, schema `ml`) or in-memory.
- **Observability.** JSON logs with `time, level, msg, service, version, env,
  trace_id, span_id, request_id`, matching `platform/logging`; secrets and
  content-bearing keys are redacted and note text is never logged.
  OpenTelemetry traces and metrics over OTLP, configured by `OTEL_*`
  (`OTEL_SDK_DISABLED=true` turns them off). The gRPC interceptor continues
  `traceparent` and `x-request-id`; the consumer continues `Event.traceparent`.
- **Lifecycle.** gRPC health (`grpc.health.v1`); on SIGTERM health goes
  NOT_SERVING, the consumer drains, then the server drains for
  `SKEIN_ML_SHUTDOWN_GRACE_SECONDS`.

Configuration variables are listed in `src/skein_ml/config.py`.

## Embedders

`HashingEmbedder` (default) is deterministic and needs no files: signed
hashing of unigrams and bigrams with sublinear TF and optional fitted IDF.
`OnnxSentenceEmbedder` loads a local sentence-transformer export
(`model.onnx` + `tokenizer.json`, e.g. all-MiniLM-L6-v2, 384-d) from
`SKEIN_ML_ONNX_MODEL_DIR` and never downloads; its test is opt-in (`-m onnx`).

## Follow-ups

- A Kubernetes manifest.
- Run the Postgres integration tests (`-m integration` with
  `SKEIN_ML_TEST_DATABASE_URL`) and the Docker build in CI.
- Per-workspace locks instead of one service-wide lock; persist built HNSW
  graphs instead of rebuilding them on startup.
