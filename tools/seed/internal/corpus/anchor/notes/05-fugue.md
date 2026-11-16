---
title: Fugue versus RGA and YATA
author: storage
editors: owner
tags: crdt, research
created: 210
---
All three are list CRDTs that give every character a unique ID and order concurrent inserts deterministically. They differ in what happens when two people type at the same position at the same time.

| Algorithm | Interleaves concurrent typing? | Notes |
| --- | --- | --- |
| RGA | Yes, in some cases | Simple, widely implemented |
| YATA | Rarely, but forward and backward typing can still interleave | Used by Yjs |
| Fugue | No (maximal non-interleaving) | Tree of left and right children |

The decisive case is two people typing "hello" and "world" at the same spot: Fugue keeps "helloworld" or "worldhello", never "hweolrllod".

> Interleaving is not a correctness bug, it is a usability bug. Nobody files a ticket; they just stop trusting the editor.

Siblings are ordered by ID, replica first and then counter. That tie-break is part of the spec, so the Rust port must match it exactly. Back to [[CRDT primer]].
