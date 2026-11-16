---
title: CRDT benchmark log
author: storage
editors: owner
tags: crdt, benchmark
created: 150
---
Numbers from the reference engine on a 2023 laptop. The reference engine walks a linked list for every local edit, so local inserts are linear in note length; the optimised engine will be compared against it operation for operation.

| Workload | Ops | Time | Notes |
| --- | --- | --- | --- |
| Type 10k characters at the end | 10,000 | 41 ms | append path |
| Type 10k characters at random positions | 10,000 | 2.3 s | linear walk dominates |
| Merge two 5k-op replicas | 10,000 | 18 ms | remote ops are cheap |
| Decode a 100k-op batch | 100,000 | 9 ms | varint decoding |

```python
import statistics

def summarize(samples_ms):
    return {
        "p50": statistics.median(samples_ms),
        "p99": sorted(samples_ms)[int(len(samples_ms) * 0.99) - 1],
    }
```

Next step: an order-statistic tree so random-position inserts stop being linear. See [[CRDT primer]].
