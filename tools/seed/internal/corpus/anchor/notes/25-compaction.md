---
title: Compaction strategies
author: storage
editors: sre
tags: lsm, storage, tuning
created: 212
---
Compaction decides the engine's three amplification factors: write, read and space. You can make any two small; not all three.

| Strategy | Write amplification | Read amplification | Space amplification |
| --- | --- | --- | --- |
| Leveled | high (10 to 30) | low | low (about 1.1) |
| Tiered | low | high | high (up to 2) |
| Hybrid (tiered L0, leveled below) | medium | medium | medium |

We run leveled compaction with a size ratio of 10 and seven levels. L0 compaction triggers at 4 files; writes slow at 20 files and stop at 36.

Spinning-disk deployments need different numbers; that work is tracked in [[Compaction tuning for HDD]]. What happens when compaction cannot keep up is in [[Write stall investigation]]. Back to [[LSM tree overview]].
