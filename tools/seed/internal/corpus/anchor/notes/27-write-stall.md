---
title: Write stall investigation
author: storage
editors: sre, owner
tags: lsm, storage, incident
created: 92
---
Working notes from the stall on 2026-08-19. The formal write-up is [[Postmortem INC-2291 write stall]].

## Timeline (UTC)

- 14:02 p99 write latency crosses 2 s
- 14:05 L0 file count reaches 36 and writes stop completely
- 14:20 compaction threads confirmed saturated at 2
- 14:38 max_background_compactions raised from 2 to 6
- 14:49 L0 back under 20 files, writes resume

## Why compaction fell behind

A backfill job wrote 3x the normal volume for two hours. Two compaction threads could not merge L0 fast enough, L0 grew, and every read had to check more overlapping files, which slowed compaction further.

```sql
SELECT date_trunc('minute', at) AS minute, max(l0_files) AS l0
FROM lsm_metrics
WHERE at BETWEEN '2026-08-19 13:30' AND '2026-08-19 15:30'
GROUP BY 1
ORDER BY 1;
```

Strategy background: [[Compaction strategies]].
