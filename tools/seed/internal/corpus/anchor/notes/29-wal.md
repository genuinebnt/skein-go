---
title: WAL recovery
author: storage
editors: sre
tags: lsm, storage, durability
created: 130
---
On startup the engine replays the write-ahead log into a fresh memtable. Records are framed with a length and a CRC32C, so a record torn by a crash is detected and recovery stops at the last complete record.

1. Open the newest WAL segment named in the manifest.
2. Read records until the end, or until a CRC mismatch.
3. A mismatch in the last 32 KiB is a torn tail: truncate and continue.
4. A mismatch anywhere else is corruption: refuse to start and page.

Group commit batches fsync calls: one fsync covers every write that arrived during the previous one, which raised write throughput 4x with no loss of durability.

Back to [[LSM tree overview]].
