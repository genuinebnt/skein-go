---
title: Pinning explained
author: storage
editors: owner
tags: rust, async
created: 200
---
An async block can hold references to its own local variables across an await, so the compiler-generated state machine may point into itself. Moving it would leave those pointers dangling. `Pin` is the promise that it will not move again.

- `Pin<&mut T>` can only be created safely if `T: Unpin` or the value is already pinned somewhere stable.
- `Box::pin` puts the future on the heap, where it never moves.
- `tokio::pin!` pins on the stack for the rest of the scope.

```rust
let sleep = tokio::time::sleep(Duration::from_secs(5));
tokio::pin!(sleep);
loop {
    tokio::select! {
        _ = &mut sleep => break,
        msg = rx.recv() => handle(msg),
    }
}
```

Most code never names `Pin` at all. When the compiler asks for it, the cause is usually a future stored in a struct or polled in a loop. Back to [[Rust async mental model]].
