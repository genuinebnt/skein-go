---
title: Cancellation safety checklist
author: storage
editors: sre
tags: rust, async, review
created: 185
---
A future is cancelled by dropping it, at any await point. `tokio::select!` drops every branch that did not win, which is where most cancellation bugs come from.

- [x] Reads from `mpsc::Receiver::recv` are cancellation safe
- [x] `tokio::time::sleep` is cancellation safe
- [ ] Audit every `select!` in the sync service for half-written state
- [ ] Replace `read_exact` inside `select!` with a buffered framed reader
- [ ] Add a test that cancels each handler at every await point

> If a function writes state, then awaits, then writes more state, cancellation can leave the first write without the second.

Part of [[Rust async mental model]]. Review notes in [[Async Rust review notes]].
