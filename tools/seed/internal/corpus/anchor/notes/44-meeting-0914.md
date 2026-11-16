---
title: Meeting notes 2026-09-14 storage sync
author: storage
editors: sre, owner
tags: meeting, storage
created: 63
---
Attendees: Kenta, Priyanka, Mira. Notes by Kenta.

## Agenda

1. Follow-ups from the August write stall
2. Block cache size
3. SSTable v2 rollout

## Decisions

- Keep max_background_compactions at 6 permanently.
- Keep the block cache at 512 MiB per node; see [[Block cache sizing]].
- Roll out [[SSTable format v2]] to one zone on 2026-09-29, the rest a week later.

## Action items

- [x] Kenta: alert when L0 exceeds 20 files
- [ ] Priyanka: rate-limit the backfill job to 50 MB/s
- [ ] Mira: decide whether backfills need an approval step

Context: [[Postmortem INC-2291 write stall]].
