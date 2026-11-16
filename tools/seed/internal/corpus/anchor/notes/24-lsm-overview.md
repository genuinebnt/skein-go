---
title: LSM tree overview
author: storage
editors: owner
tags: lsm, storage, primer
created: 228
---
A log-structured merge tree turns random writes into sequential ones. Writes go to a write-ahead log and an in-memory memtable; full memtables are flushed as sorted, immutable SSTables; background compaction merges SSTables into larger ones level by level.

## Read path

1. Check the memtable.
2. Check L0 files, newest first (they can overlap).
3. For each deeper level, binary search the one file whose key range covers the key, skipping it if its bloom filter says no.

## Write path

1. Append to the WAL and fsync (or group commit).
2. Insert into the memtable.
3. When the memtable reaches 64 MiB, freeze it and flush it to L0.

Deeper notes: [[Compaction strategies]], [[Bloom filter sizing]], [[SSTable format v2]], [[WAL recovery]], [[Block cache sizing]].
