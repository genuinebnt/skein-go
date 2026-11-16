---
title: Vector search overview
author: ml
editors: owner
tags: vector-search, search, primer
created: 218
acl: guest=editor
---
Semantic search embeds every block and finds the blocks whose vectors are closest to the query's. Exact search compares the query with every vector; approximate indexes trade a little recall for much less work.

## Pieces

- Embedding model and normalisation: [[Embedding model choice]]
- Graph index: [[HNSW parameters]]
- Inverted file and compression: [[IVF and product quantization]]
- Measuring what we lose: [[Recall evaluation harness]]
- Combining with keyword search: [[Hybrid retrieval with BM25]]

Every approximate result is filtered by permission before it is returned, and the filter runs before truncating to k, not after; otherwise a user with access to few notes gets fewer than k results.

> Recall without a baseline is a number without a meaning. Always report it against a brute-force scan.
