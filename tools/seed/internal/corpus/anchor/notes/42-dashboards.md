---
title: Dashboards we actually use
author: sre
editors: owner
tags: observability, dashboards
created: 100
---
We had 41 dashboards. These four are the ones anyone opened in the last quarter.

1. Sync overview: connected replicas, ops per second, rejected batches, p99 apply latency
2. Search: queries per second, trigram candidate ratio, p95 latency by mode
3. Storage engine: L0 files, compaction throughput, stall seconds, block cache hit rate
4. Agent runs: runs per hour, tool errors, confirmation rate, tokens per run

The rest were archived. Anyone can restore one, but it then needs an owner. Back to [[Observability conventions]].
