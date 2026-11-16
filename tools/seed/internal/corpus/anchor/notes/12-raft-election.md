---
title: Raft leader election timeouts
author: sre
editors: admin
tags: raft, consensus, tuning
created: 205
---
A follower that hears nothing from a leader for one election timeout becomes a candidate and asks for votes. If every follower used the same timeout they would all stand at once and split the vote, so each picks a fresh random timeout.

## Our settings

- Election timeout is randomised between 150 and 300 ms, drawn again after every election.
- The leader sends a heartbeat every 50 ms.
- A follower that receives a heartbeat with a higher term steps down immediately.

```go
func (n *Node) resetElectionTimer() {
	timeout := 150*time.Millisecond + time.Duration(n.rand.Int63n(int64(150*time.Millisecond)))
	n.electionTimer.Reset(timeout)
}
```

The timeout has to stay well above the worst pause a node can take; [[Postmortem INC-2317 election storm]] is what happened when a garbage collection pause did not. Part of [[Raft overview]].
