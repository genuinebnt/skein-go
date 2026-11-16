---
title: Block cache sizing
author: sre
editors: storage
tags: lsm, storage, caching
created: 110
---
The block cache holds uncompressed data blocks. It is sized per node, not per table.

- Current size is 512 MiB per node, with a steady hit rate of 94 percent.
- Doubling it to 1 GiB raised the hit rate to 96 percent: not worth the memory.
- Index and filter blocks are pinned in the cache so a miss never costs two reads.

| Cache size | Hit rate | p99 read latency |
| --- | --- | --- |
| 256 MiB | 88% | 9.1 ms |
| 512 MiB | 94% | 4.2 ms |
| 1 GiB | 96% | 3.8 ms |

Eviction is LRU with a small admission window; a scan does not flush hot blocks. Back to [[LSM tree overview]].
