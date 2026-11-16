---
title: Membership changes with joint consensus
author: admin
editors: owner
tags: raft, consensus
created: 180
---
Changing the voter set in one step can produce two disjoint majorities. Joint consensus avoids that: the cluster first moves to a configuration that needs a majority of the old set and a majority of the new set, then to the new set alone.

1. Add the new node as a learner and let it catch up.
2. Propose the joint configuration.
3. Once committed, propose the new configuration.
4. Remove the old node only after the new configuration commits.

- [x] Learner support
- [x] Joint configuration in the log
- [ ] Automatic removal of nodes that stay unreachable for a day

Never remove two voters at once from a five-node cluster during an incident. Part of [[Raft overview]]; procedures in [[Raft cluster runbook]].
