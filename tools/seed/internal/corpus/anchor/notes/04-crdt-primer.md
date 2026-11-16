---
title: CRDT primer
author: owner
editors: storage, admin
tags: crdt, replication, primer
created: 220
---
A conflict-free replicated data type lets every replica edit locally and still converge: replicas that have applied the same set of operations show the same state, whatever order the operations arrived in.

## Two families

1. State-based CRDTs ship the whole state and merge with a join (a least upper bound).
2. Operation-based CRDTs ship operations and need causal delivery, but messages stay small.

We use an operation-based sequence because notes are long and edits are tiny. The sequence algorithm is Fugue; [[Fugue versus RGA and YATA]] explains the choice.

## What has to hold

- Commutativity of concurrent operations
- Idempotence, because the network duplicates messages
- Causal delivery, provided by a buffer in front of the sequence

```rust
pub trait Crdt {
    type Op;
    fn apply(&mut self, op: &Self::Op) -> Result<(), ApplyError>;
    fn state_hash(&self) -> u64;
}
```

Deletes leave tombstones behind; [[Tombstones and garbage collection]] covers when they can go. Replicas compare [[Version vectors in practice]] to decide what to send each other.
