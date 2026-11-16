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
