---
title: Release checklist v0.4
author: admin
editors: sre
tags: release, process
created: 28
---
Release 0.4.0 ships on 2026-11-10. Release captain: Tobias.

## Before

- [x] Freeze the branch on 2026-11-05
- [x] Run the chaos suite for 24 hours
- [ ] Load test at 3x write volume (from [[Postmortem INC-2291 write stall]])
- [ ] Update the status page template

## Deploy

```yaml
release:
  version: 0.4.0
  date: 2026-11-10
  strategy: canary
  canary:
    steps: [5, 25, 50, 100]
    pause: 30m
  rollback_on:
    - SyncAvailabilityFastBurn
```

## After

- [ ] Announce in the changelog
- [ ] Close the milestone
