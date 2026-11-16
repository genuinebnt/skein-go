---
title: Glossary
author: admin
editors: owner, storage
tags: reference, meta
created: 235
---
Terms we use without explaining. Keep entries short and link to the note that explains each one properly.

| Term | Meaning | Where to read more |
| --- | --- | --- |
| Replica | One device or process holding a full copy of a note | [[CRDT primer]] |
| Tombstone | A deleted element that stays in the structure | [[Tombstones and garbage collection]] |
| Version vector | Per-replica counter of the last applied op | [[Version vectors in practice]] |
| Term | Raft's logical epoch; one leader at most per term | [[Raft overview]] |
| L0 | The first, unsorted level of an LSM tree | [[LSM tree overview]] |
| Write stall | Writes paused because compaction is behind | [[Write stall investigation]] |
| ef_search | HNSW query-time beam width | [[HNSW parameters]] |
| Burn rate | How fast an SLO's error budget is being spent | [[SLOs and burn-rate alerts]] |

A **block** is one paragraph, heading, list item or code block. A **marker** is the invisible element that starts a block; see [[Block markers proposal]].
