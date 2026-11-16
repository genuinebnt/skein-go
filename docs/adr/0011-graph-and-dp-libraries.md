# 0011. Graph algorithms and dynamic programming as pure libraries

Date: 2026-11-16. Status: accepted.

## Decision
- `libs/graph`: the note link graph and its algorithms. Breadth-first search,
  blast radius (everything reachable by following outbound links, by hop),
  connected components and orphans, Tarjan's strongly connected components
  with stable numbering, cycle finding, topological order with the unplaceable
  remainder, Dijkstra over semantic distance, betweenness centrality,
  PageRank, Louvain communities, reading paths.
- `libs/dp`: Levenshtein and Damerau distance (with a banded variant), LCS,
  Myers diff, a reading plan solver (knapsack over notes with prerequisites,
  under a time budget), optimal chunking and best snippet windows.
- Both are pure: no I/O, deterministic results (ties broken by ID), and every
  algorithm has a brute-force oracle in its tests.
- `search` serves them over gRPC; the graph explorer UI shows their results
  with the numbers behind them.
- Blast radius takes a direction (`BlastDirection` in `search.proto`): the default, `CITED_BY`, follows inbound links ("if this note changes, these notes are affected"); `CITES` follows outbound links as described above.
