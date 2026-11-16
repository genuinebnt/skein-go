---
title: Backpressure with bounded channels
author: storage
editors: owner
tags: rust, async, design
created: 160
---
An unbounded channel turns a slow consumer into a memory leak. A bounded channel makes the producer wait, which pushes the slowdown back to whoever is sending.

## Rules

1. Every channel between tasks is bounded; the bound is written next to it with a reason.
2. Per-connection send queues hold 256 messages; a client that falls further behind is disconnected and resyncs.
3. Never use `try_send` and drop silently: count every drop in a metric.

```rust
let (tx, mut rx) = tokio::sync::mpsc::channel::<Frame>(256);
tokio::spawn(async move {
    while let Some(frame) = rx.recv().await {
        socket.send(frame).await?;
    }
    Ok::<_, io::Error>(())
});
```

Part of [[Rust async mental model]].
