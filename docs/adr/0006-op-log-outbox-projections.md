# 0006. Op log as event store, transactional outbox, projections

Date: 2026-11-16. Status: accepted.

## Context
CRDT operations are immutable events, and the document is their replay.
Several services need to react to document changes: search, ML, notifications
and the agent. Publishing to Pub/Sub after a database commit can lose events
on a crash; publishing before can announce changes that never committed.

## Decision
- The `documents` op log is append-only and is the source of truth, with
  periodic snapshots as checkpoints.
- Every service that publishes events uses a transactional outbox: state
  change and outbox row in one transaction, then a relay publishes in order
  and marks rows published.
- Consumers are idempotent, recording processed event IDs in the same
  transaction as their effect.
- Search indexes, embeddings, clusters and the link graph are projections:
  each records the last event it applied and can be rebuilt by replaying.
- Events carry the note ID as Pub/Sub ordering key.

## Consequences
- Delivery is at least once; duplicates are expected and harmless.
- Projections lag the write path by the relay and consumer delay. The UI says
  when a result may be stale.
- Erasing data (account deletion, legal requests) needs an explicit purge
  path through the op log, snapshots and every projection. It is designed in
  the documents milestone.
