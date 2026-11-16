---
title: Raft overview
author: admin
editors: sre, owner
tags: raft, consensus, primer
created: 225
---
Raft keeps a replicated log consistent across a cluster by electing one leader per term. The leader accepts writes, appends them to its log and replicates them; an entry is committed once a majority has persisted it.

## Moving parts

- Leader election, with randomised timeouts: [[Raft leader election timeouts]]
- Log replication through AppendEntries, with consistency checks on the previous index and term
- Log compaction with snapshots: [[Raft log compaction and snapshots]]
- Membership changes: [[Membership changes with joint consensus]]
- Reads that stay linearizable: [[Linearizable reads with ReadIndex]]

Our metadata cluster runs five voters across three zones, which tolerates the loss of two voters or one zone.

> Raft is not simpler than Paxos because the problem is simpler. It is simpler because it picks one way to do each thing and explains it.

Operational procedures are in [[Raft cluster runbook]].
