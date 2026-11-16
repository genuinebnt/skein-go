# Performance log

Every optimisation gets an entry: the hypothesis, the change, and numbers
before and after on the same machine. Reverting a change that did not help is
a valid entry.

## Baselines

### libs/crdt reference implementation (2026-11-16)

Go 1.24.7, linux/amd64, 2 vCPUs (cloud development container). `go test
-bench=. -benchtime=20x`. Each run builds a fresh document.

| Benchmark | Work per op | Time per op |
|---|---|---|
| `BenchmarkAppendTyping` | type 2,000 characters at the end | 7.1 ms |
| `BenchmarkRandomEdits` | 2,000 random inserts and deletes | 13.0 ms |
| `BenchmarkMergeConcurrentHistories` | merge two 1,000-edit histories into a fresh replica | 2.6 ms |
| `BenchmarkEncodeDecode` | encode and decode a 2,000-edit history | 0.66 ms |

Local edits are O(n) per index lookup, so append typing is quadratic overall
(about 3.5 µs per character at 2,000 characters). The planned run-length,
order-statistic-tree representation targets O(log n) and will be measured on
the public editing traces as well.

### libs/graph reference implementation (2026-11-16)

Go 1.24.7, linux/amd64, 2 vCPUs (cloud development container). `go test
-run='^$' -bench=. -benchmem` in `libs/graph`. The workload is a 10,000-note
graph with five random links per note (49,985 directed edges after removing
repeats, 49,972 undirected, one connected component, seed 1), built once and
shared by every benchmark except `New`.

| Benchmark | Work per op | Time per op | Allocated per op |
|---|---|---|---|
| `BenchmarkNew` | validate and build the graph | 21 ms | 10.0 MB |
| `BenchmarkDistances` | undirected BFS from one note | 1.8 ms | 1.3 MB |
| `BenchmarkBlastRadius` | `CitedBy` BFS from one note | 1.7 ms | 1.3 MB |
| `BenchmarkConnectedComponents` | flood fill | 1.8 ms | 2.1 MB |
| `BenchmarkStronglyConnected` | iterative Tarjan | 3.0 ms | 2.8 MB |
| `BenchmarkFindCycle` | three-colour DFS to the first back edge | 3.7 µs | 11 KB |
| `BenchmarkTopologicalOrder` | Kahn with a min-heap | 0.30 ms | 0.8 MB |
| `BenchmarkShortestWeightedPath` | Dijkstra, one pair | 8.6 ms | 2.6 MB |
| `BenchmarkBetweenness` | Brandes, every source | 15.9 s | 0.8 MB |
| `BenchmarkPageRank` | power iteration to 1e-10 | 5.9 ms | 0.6 MB |
| `BenchmarkLouvain` | all levels | 186 ms | 23 MB |
| `BenchmarkReadingPath` | prerequisites of one note | 3.0 ms | 2.7 MB |
| `BenchmarkDiameter` | BFS from every note | 6.2 s | 4.4 GB |

Most of the per-query time in the linear algorithms is building the result
map from string IDs, not the traversal. Betweenness and Diameter are O(V E)
and too slow to run per request at this size; the search service should
cache them per graph version or use sampled Brandes and double-sweep bounds
(a follow-up, not done here).

Tried: hoisting the division out of Brandes' dependency loop (one division
per node instead of per edge). 16.2 s and 15.7 s against 15.9 s: no
measurable effect, the loop is bound by memory access. Reverted.

### libs/dp reference implementations (2026-11-16)

Go 1.24.7, linux/amd64, 2 vCPUs (Intel Xeon @ 2.10GHz, cloud development
container). `go test -run='^$' -bench=. -benchmem`. Workloads are seeded and
defined in `libs/dp/bench_test.go`; "edited copy" changes about one word in
fifty.

| Benchmark | Work per op | Time per op | Memory per op |
|---|---|---|---|
| `BenchmarkLevenshtein100` | two unrelated 100-rune strings | 28.6 µs | 2.6 KB |
| `BenchmarkLevenshteinWithinK2Of100` | 100 runes vs an edited copy, k = 2 | 1.0 µs | 0.8 KB |
| `BenchmarkDamerauLevenshtein30` | two unrelated 30-rune strings | 17.7 µs | 9.1 KB |
| `BenchmarkLCS2000` | Hirschberg LCS, 2,000 × 2,000 elements, alphabet 20 | 18.0 ms | 0.8 MB |
| `BenchmarkDiffLines/myers/2000words` | line diff, 2,000-word note vs edited copy | 121 µs | 110 KB |
| `BenchmarkDiffLines/patience/2000words` | same, patience | 153 µs | 107 KB |
| `BenchmarkDiffLines/chars/2000words` | character diff, same texts | 5.7 ms | 4.0 MB |
| `BenchmarkDiffLines/myers/20000words` | line diff, 20,000 words | 3.6 ms | 1.5 MB |
| `BenchmarkDiffLines/patience/20000words` | same, patience | 2.6 ms | 1.2 MB |
| `BenchmarkDiffLines/chars/20000words` | character diff, ~120,000 code points | 142 ms | 55 MB |
| `BenchmarkPlanReading/tree-dp/300notes/120min` | forest of 300 notes, 120-minute budget | 0.80 ms | 0.97 MB |
| `BenchmarkPlanReading/branch-and-bound/40notes/60min` | 40-note DAG, 60 minutes (exact, 3,283 nodes) | 0.64 ms | 68 KB |
| `BenchmarkChunkBlocks1000` | 1,000 blocks of 10–129 tokens, limit 512 | 35 µs | 29 KB |
| `BenchmarkBestSnippet200` | 200 matches over 5,000 code points, window 160 | 49 µs | 7 KB |
| `BenchmarkSegment` | "rustasyncwebsocketgolang", 15-word dictionary | 4.2 µs | 0.9 KB |

Character diffs of whole notes are the expensive case: the diff records one
operation per element before grouping, so memory is about 450 bytes per code
point at this size. The documents service should diff by line first and
refine changed lines by word or character, which keeps D and N small.

### services/ml algorithms (2026-11-16)

Python 3.13, NumPy 2.5, 2 vCPUs (cloud development container). Clustering
and vector search, from scratch and against scikit-learn, FAISS and hnswlib,
are recorded in [ml-benchmarks.md](ml-benchmarks.md) by
`python -m skein_ml.bench`; regenerate that file rather than copying numbers
here. Headline: scratch e-means reproduces `faiss.contrib.e_means` exactly;
Elkan does 3 to 10 times fewer distance computations than Lloyd from the same
start; scratch HNSW matches hnswlib's recall@10 at equal parameters.

### libs/trigram and services/search (2026-11-16)

Go 1.24.7, 2 vCPUs (Intel Xeon 2.1 GHz, cloud development container),
`go test -bench`. Synthetic workload: 10,000 blocks of 30 words from a
12-word vocabulary, every fifth a Go function.

| Benchmark | Time | Memory |
|---|---|---|
| `BenchmarkAdd10k` (build the index) | 199 ms | 78 MB, 401k allocs |
| `BenchmarkLiteral10k` ("compaction snapshot", candidates confirmed) | 8.2 ms | 3.2 MB |
| `BenchmarkRegexp10k` (`func \(\w+ \*Store\d+\) Close\(ctx ...` with `languages=go`) | 4.2 ms | 1.1 MB |
| `BenchmarkSnapshot10k` (encode and decode the index) | 22 ms | 24 MB |

Decoding a snapshot is about nine times cheaper than rebuilding, which is
why the service persists the trigram index rather than only the text.

On the small seed (300 notes, 3,923 blocks; `services/search/internal/seed`):
restoring the projection from a snapshot takes 124 ms, loading it from the
store and rebuilding takes 205 ms (in-memory store, so Postgres I/O is
excluded and the real gap is larger; the rest of the snapshot path is
rebuilding the word and title indexes). The regex truth queries read 163
candidate blocks in total for 153 matches.
