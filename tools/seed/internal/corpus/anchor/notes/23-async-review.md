---
title: Async Rust review notes
author: owner
editors: storage, sre
tags: rust, async, review
created: 120
---
Findings from reviewing the sync service before the beta. Ordered by risk.

1. A `std::sync::Mutex` guard was held across an await in the presence handler; replaced with `tokio::sync::Mutex`.
2. Two `select!` loops lost a partially read frame on cancellation; see [[Cancellation safety checklist]].
3. Snapshot compression ran on a worker thread and stalled heartbeats for up to 80 ms; moved to `spawn_blocking`.
4. An unbounded channel fed the indexer; bounded at 1,024 (see [[Backpressure with bounded channels]]).

> None of these showed up in unit tests. All of them showed up under the chaos harness within an hour.

Follow-up owner: Kenta.
