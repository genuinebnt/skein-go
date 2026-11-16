# tools/seed

Deterministic generator of Skein seed datasets with ground truth.

```
go run ./tools/seed -profile small|medium|large|stress -seed 1234 -out <dir> [-workers N] [-quiet]
make seed PROFILE=medium      # or seed-small, seed-check
```

Everything about the output (profiles and their exact counts, layout,
formats, ground truth definitions, loader contract) is in
[docs/seed-data.md](../../docs/seed-data.md).

## Packages

| Package | Role |
|---|---|
| `internal/generate` | the pipeline: plan, realize notes in parallel, write in order, ground truth, manifest |
| `internal/history` | edit sessions on real `libs/crdt` replicas, delivery to a server replica, replay verification |
| `internal/blocks` | block layout in a note's sequence (U+001E markers) and the anchor Markdown parser |
| `internal/corpus` | invented people and companies, topic models, code templates, multilingual snippets |
| `internal/corpus/anchor` | the hand-written anchor workspace (`notes/*.md`) and agent evals |
| `internal/policy` | the access policy the permission truth assumes |
| `internal/graphs` | Tarjan, components, BFS rings, shortest paths for graph truth |
| `internal/hashembed` | skein-hash-v1, the deterministic stand-in embedder |
| `internal/prng`, `internal/uuid7`, `internal/jsonl` | named random streams, deterministic UUIDv7, hashed JSON Lines writers |

## Tests

`go test ./...` from this directory (or `make seed-check`, which also runs
the medium profile against its time budget). The tests in
`internal/generate` read `testdata/seed/small` like an outside loader and
recompute every piece of ground truth with independent code.
