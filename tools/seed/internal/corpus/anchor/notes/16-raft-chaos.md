---
title: Raft chaos test plan
author: sre
editors: admin, storage
tags: raft, testing, chaos
created: 160
---
We run the consensus layer inside a simulated network that delays, drops, duplicates and reorders messages, driven by a seed so every failure replays.

## Faults

- Partition the leader from a minority, then from a majority
- Pause a node for longer than the election timeout
- Restart a node with its last 100 log entries truncated
- Skew one node's clock by two seconds

## Checks after every step

- At most one leader per term
- Committed entries never change
- Every node's applied log is a prefix of the leader's

```yaml
scenario: leader-isolation
seed: 4242
steps:
  - partition: [n1]
  - wait: 2s
  - heal: all
expect:
  leaders_per_term: 1
```

Background: [[Raft overview]], [[Raft leader election timeouts]].
