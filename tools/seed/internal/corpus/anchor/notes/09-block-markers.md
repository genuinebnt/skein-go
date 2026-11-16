---
title: Block markers proposal
author: storage
editors: owner
tags: crdt, design, blocks
created: 170
---
Proposal: one sequence per note, with a marker element at the start of every block. Pressing Enter inserts a marker; Backspace at the start of a block deletes one. Concurrent typing inside a block always survives a split or merge, because no text is copied.

## Attributes

Each marker carries last-writer-wins attributes: block type, indent level, checked state and code language, ordered by Lamport timestamp and then replica.

| Attribute | Example | Conflict rule |
| --- | --- | --- |
| type | heading2 | last writer wins |
| indent | 3 | last writer wins |
| checked | true | last writer wins |
| language | rust | last writer wins |

Until the wire format has a marker operation, the seed data stores the marker as an ordinary character. Background in [[CRDT primer]] and [[Fugue versus RGA and YATA]].
