---
title: Bloom filter sizing
author: storage
editors: ml
tags: lsm, storage, probabilistic
created: 198
---
A bloom filter answers "definitely not present" or "maybe present". Each SSTable carries one, so a point read can skip files without touching them.

## Sizing

With k hash functions and m bits per key, the false positive rate is about (1 - e^(-k/m))^k. We use 10 bits per key with 7 hash functions, which gives roughly a 1 percent false positive rate. Going to 16 bits per key drops it to about 0.05 percent for 60 percent more filter memory.

```python
import math

def false_positive_rate(bits_per_key: float, hashes: int) -> float:
    return (1 - math.exp(-hashes / bits_per_key)) ** hashes

print(round(false_positive_rate(10, 7), 4))  # 0.0082
```

Filters for the last level are skipped: most reads that reach it find the key. Back to [[LSM tree overview]].
