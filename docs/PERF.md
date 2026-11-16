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

