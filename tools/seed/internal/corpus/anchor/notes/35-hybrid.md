---
title: Hybrid retrieval with BM25
author: ml
editors: owner
tags: vector-search, search
created: 140
---
Keyword search wins on exact identifiers and rare words; semantic search wins on paraphrases. We run both and merge with reciprocal rank fusion with k = 60, which needs no score calibration.

| Query type | BM25 nDCG@10 | Semantic nDCG@10 | Fused nDCG@10 |
| --- | --- | --- | --- |
| Identifiers (function names) | 0.81 | 0.42 | 0.80 |
| Natural language questions | 0.48 | 0.71 | 0.74 |

The discovery panel shows the three signals separately and never blends them into one score, so a user can see why something was suggested. Back to [[Vector search overview]].
