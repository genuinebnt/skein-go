---
title: SSTable format v2
author: storage
editors: owner
tags: lsm, storage, format
created: 140
---
Version 2 of our SSTable layout. All integers are little-endian unless marked varint.

| Section | Contents |
| --- | --- |
| Data blocks | 16 KiB target, prefix-compressed keys, restart point every 16 keys |
| Filter block | one bloom filter for the whole file |
| Index block | last key of each data block and its offset |
| Footer | offsets of filter and index, format version, CRC32C of the footer |

Changes from v1:

- zstd compression per block instead of snappy
- CRC32C per block instead of per file, so a torn write is detected at the block that holds it
- The footer moved to a fixed 64 bytes so readers find it with one seek

```go
type Footer struct {
	FilterOffset uint64
	FilterSize   uint64
	IndexOffset  uint64
	IndexSize    uint64
	Version      uint32
	Checksum     uint32
}
```

Raft snapshots reuse this format ([[Raft log compaction and snapshots]]). Back to [[LSM tree overview]].
