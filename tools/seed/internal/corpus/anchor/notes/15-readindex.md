---
title: Linearizable reads with ReadIndex
author: sre
editors: admin
tags: raft, consensus, reads
created: 175
---
A leader cannot answer reads from local state blindly: it might have been deposed without knowing. ReadIndex fixes that without writing to the log.

1. Record the current commit index as the read index.
2. Send a round of heartbeats and wait for a majority to answer, which proves leadership.
3. Wait until the state machine has applied up to the read index.
4. Serve the read.

Lease reads skip step 2 by trusting clocks for a bounded lease. We do not use them: clock drift on the old hypervisors was measured at up to 400 ms.

Part of [[Raft overview]].
