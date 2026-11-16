---
title: Version vectors in practice
author: owner
editors: storage
tags: crdt, replication
created: 195
---
A version vector maps each replica to the counter of its last applied operation. Because a replica's operations are applied in counter order, the vector describes the applied set exactly.

## Sizes we see

| Notes | Median replicas | 99th percentile replicas |
| --- | --- | --- |
| Personal | 2 | 6 |
| Team | 7 | 41 |

The tail comes from people who open a note on many devices and from agent runs, which each get their own replica.

```go
func missing(local, remote VersionVector, log []Op) []Op {
	var out []Op
	for _, op := range log {
		if op.ID.Counter > remote[op.ID.Replica] {
			out = append(out, op)
		}
	}
	return out
}
```

Compaction folds retired replicas into a snapshot so vectors stop growing without bound. Related: [[Tombstones and garbage collection]], [[Offline sync design]].
