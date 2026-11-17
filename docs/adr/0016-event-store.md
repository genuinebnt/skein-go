# 0016. Postgres event logs as the event store; Pub/Sub stays the broker

Date: 2026-11-17. Status: accepted. Refines [0006](0006-op-log-outbox-projections.md).

## Context
Pub/Sub delivers events but does not keep them: an acknowledged message is
gone, and topic retention tops out at 31 days. Search's trigram index and
link graph, ml's embeddings and clusters, and notifications' inbox are all
projections of other services' events, yet nothing can replay the full
history to rebuild one after data loss, a bug, or a new feature. Only the
documents op log is a true event store today; each service's outbox rows
are published and then forgotten.

## Decision
- Each publishing service keeps its outbox rows permanently as its event
  log: an append-only `events` table in its own schema with a gap-free
  sequence number (`seq`), the full `Event` envelope, and the publish state.
  Rows are never updated except to mark them published and never deleted
  (see Consequences for erasure).
- Each publishing service exposes `ReadEvents(after_seq, limit)` as a
  server-streaming RPC (internal, no REST mapping) that streams its log in
  order.
- Projections store `(source, last_seq)` as their checkpoint. To rebuild, a
  projection resets its checkpoint to 0, catches up through `ReadEvents`,
  then switches to the Pub/Sub subscription and drops any event with
  `seq <= last_seq` (duplicates are already idempotent). Catch-up and live
  delivery share one handler.
- Pub/Sub remains the transport for live delivery, ordering by note ID and
  dead-lettering. Message ordering and the outbox relay are unchanged.
- The platform `events` package gains the event-log table, `ReadEvents`
  server and client, and a catch-up subscriber; services adopt them without
  bespoke code.
- Event schemas follow `events.proto` and only grow compatibly (new fields,
  new payload cases), because old events are replayed by new code.

## Consequences
- Rebuilding any projection is a supported operation (`make rebuild
  PROJECTION=search.trigram` locally; a Cloud Run job in GCP).
- Event logs grow without bound. Old partitions are archived to Cloud
  Storage (ADR-0019) as compressed, checksummed segments, and `ReadEvents`
  reads archived segments transparently.
- Erasure (account deletion, legal requests) cannot simply delete events:
  `NotePurged` and a future `UserErased` event carry the instruction, every
  projection erases on receipt, and the log's payloads for that subject are
  redacted in place (the envelope and sequence stay, so ordering holds).
- A Kafka adapter behind the same `Publisher`/`Subscriber` interfaces stays
  possible later (retained topics would then make `ReadEvents` redundant);
  it is a learning option, not a requirement.
