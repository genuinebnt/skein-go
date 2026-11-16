---
title: Tokio runtime tuning
author: sre
editors: storage
tags: rust, async, performance
created: 170
---
Defaults are good. These are the only settings we changed, and why.

| Setting | Value | Why |
| --- | --- | --- |
| worker_threads | 8 | the sync service shares 16-core hosts with the indexer |
| max_blocking_threads | 64 | snapshot compression uses spawn_blocking |
| event_interval | 31 (default) | changing it made fairness worse |
| global_queue_interval | 61 (default) | same |

```rust
let runtime = tokio::runtime::Builder::new_multi_thread()
    .worker_threads(8)
    .max_blocking_threads(64)
    .enable_all()
    .build()?;
```

Measure with tokio-console before changing anything else. Back to [[Rust async mental model]].
