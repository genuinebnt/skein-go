---
title: Rust async mental model
author: storage
editors: owner
tags: rust, async, primer
created: 215
---
A future is a state machine that does nothing until an executor polls it. Polling either finishes with a value or returns Pending after arranging for a waker to be called when progress is possible.

## Three rules that prevent most bugs

1. Never block inside async code: no `std::thread::sleep`, no synchronous file I/O on the runtime threads.
2. Never hold a `std::sync::Mutex` guard across an `.await`.
3. Assume any `.await` can be the last line that runs: cancellation is dropping the future.

```rust
async fn fetch_all(urls: Vec<String>) -> Vec<Result<String, reqwest::Error>> {
    let tasks = urls.into_iter().map(|u| tokio::spawn(async move {
        reqwest::get(&u).await?.text().await
    }));
    let mut out = Vec::new();
    for t in tasks {
        out.push(t.await.expect("task panicked"));
    }
    out
}
```

Deeper dives: [[Pinning explained]], [[Cancellation safety checklist]], [[Tokio runtime tuning]], [[Backpressure with bounded channels]].
