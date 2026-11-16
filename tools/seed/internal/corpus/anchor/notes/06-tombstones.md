---
title: Tombstones and garbage collection
author: storage
editors: owner, sre
tags: crdt, storage
created: 200
---
A deleted character stays in the tree as a tombstone, because an insert generated concurrently with the delete may name it as a parent. Removing it too early makes that insert impossible to place.

## When a tombstone can go

A tombstone is collectable once every replica that could still reference it has seen the delete. In practice that means a stable version vector: the minimum over all live replicas.

- Snapshot the note at the stable vector
- Drop tombstones whose delete is covered by it
- Retire replicas that have not synced for 90 days, after a final warning

Today the live notes carry about 18 percent tombstones by element count. We are not collecting yet; the plan is in [[Version vectors in practice]].

See also [[CRDT primer]] for why deletes are marks, not removals.
