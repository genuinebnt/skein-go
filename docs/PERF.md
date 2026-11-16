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

### services/ml algorithms (2026-11-16)

Python 3.13, NumPy 2.5, 2 vCPUs (cloud development container). Clustering
and vector search, from scratch and against scikit-learn, FAISS and hnswlib,
are recorded in [ml-benchmarks.md](ml-benchmarks.md) by
`python -m skein_ml.bench`; regenerate that file rather than copying numbers
here. Headline: scratch e-means reproduces `faiss.contrib.e_means` exactly;
Elkan does 3 to 10 times fewer distance computations than Lloyd from the same
start; scratch HNSW matches hnswlib's recall@10 at equal parameters.
