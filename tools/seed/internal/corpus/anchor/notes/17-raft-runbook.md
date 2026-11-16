---
title: Raft cluster runbook
author: sre
editors: admin
tags: raft, runbook, operations
created: 150
acl: viewer=viewer
---
Procedures for the metadata cluster. Run every command from the bastion; never edit the data directory by hand.

## Replace a failed node

1. Confirm the node is really gone: no heartbeats for ten minutes and the host is unreachable.
2. Start the replacement as a learner with a new node ID.
3. Wait until its match index is within 1,000 entries of the leader.
4. Promote it with a joint configuration change (see [[Membership changes with joint consensus]]).
5. Remove the failed node.

## Leader keeps changing

Check for long pauses first: garbage collection, swap, or a noisy neighbour. The election timeout is 150 to 300 ms, so any pause longer than that triggers an election. See [[Postmortem INC-2317 election storm]].

```sql
SELECT node_id, term, role, last_heartbeat_at
FROM raft_status
ORDER BY term DESC, node_id
LIMIT 20;
```

Overview: [[Raft overview]].
