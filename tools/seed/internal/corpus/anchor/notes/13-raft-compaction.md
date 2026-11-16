---
title: Raft log compaction and snapshots
author: admin
editors: sre
tags: raft, consensus, storage
created: 190
---
The log cannot grow forever. Each node snapshots its state machine and discards log entries the snapshot covers. A follower that falls behind the leader's oldest retained entry receives the snapshot through InstallSnapshot instead.

## Thresholds

- Snapshot after 10,000 applied entries or 64 MiB of log, whichever comes first
- Keep 1,000 entries after the snapshot so slightly slow followers catch up from the log
- Stream snapshots in 1 MiB chunks so a large one does not block heartbeats

```rust
fn should_snapshot(applied_since: u64, log_bytes: u64) -> bool {
    applied_since >= 10_000 || log_bytes >= 64 * 1024 * 1024
}
```

The snapshot format is the same SSTable layout as the storage engine; see [[SSTable format v2]]. Back to [[Raft overview]].
