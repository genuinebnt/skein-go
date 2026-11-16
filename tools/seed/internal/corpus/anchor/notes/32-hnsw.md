---
title: HNSW parameters
author: ml
editors: storage
tags: vector-search, tuning
created: 176
---
Results of the October parameter sweep on the 1M-vector benchmark set (768 dimensions, cosine).

| M | ef_construction | ef_search | Recall@10 | p99 latency |
| --- | --- | --- | --- | --- |
| 16 | 100 | 64 | 0.912 | 1.1 ms |
| 16 | 200 | 128 | 0.948 | 1.9 ms |
| 32 | 200 | 128 | 0.973 | 2.4 ms |
| 48 | 400 | 256 | 0.986 | 5.2 ms |

We settled on M = 32, ef_construction = 200 and ef_search = 128, for a recall@10 of 0.973 at 2.4 ms p99. Memory is about 1.4 times the raw vectors.

```python
import hnswlib

index = hnswlib.Index(space="cosine", dim=768)
index.init_index(max_elements=1_000_000, M=32, ef_construction=200)
index.set_ef(128)
```

Back to [[Vector search overview]].
