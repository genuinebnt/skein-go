---
title: Recall evaluation harness
author: ml
editors: sre
tags: vector-search, testing
created: 150
---
Every index change is measured against an exact scan on the same queries.

1. Sample 1,000 queries from real search logs (titles only, no note text).
2. Compute the exact top 10 with a brute-force scan.
3. Run the candidate index and compute recall@10 per query.
4. Report the mean and the 5th percentile, because a good mean can hide queries with zero recall.

```python
def recall_at_k(exact: list[set[str]], approx: list[list[str]], k: int = 10) -> float:
    hits = [len(e & set(a[:k])) / k for e, a in zip(exact, approx)]
    return sum(hits) / len(hits)
```

Back to [[Vector search overview]].
