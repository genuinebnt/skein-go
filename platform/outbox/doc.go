// Package outbox implements the transactional outbox and idempotent
// consumers of ADR-0006: an event is stored in the same transaction as the
// state change that caused it, a relay publishes stored events in order,
// and consumers apply each event's effect at most once even though
// delivery is at least once.
//
// # Writing events
//
// Enqueue(ctx, tx, topic, envelope) inserts a row into the service's outbox
// table using the caller's transaction, so the row commits or rolls back
// with the change itself. The envelope's traceparent is captured from ctx
// at this point, so the consumer's span continues the request that made
// the change, not the relay's background loop. SchemaSQL holds the tables;
// copy it into one of the service's migrations (a copy, because applied
// migrations must never change when this package does).
//
// # Relaying
//
// Relay.RunOnce claims up to BatchSize due rows in id order inside one
// transaction (SELECT ... FOR UPDATE SKIP LOCKED), publishes them, records
// the outcome and commits. Run loops over RunOnce until its context ends,
// immediately while batches come back full, otherwise every PollInterval
// or when Notify is called after a commit.
//
//   - Order. Rows with the same ordering key are published one at a time,
//     in id order. When one fails, the rest of its key waits: in this batch
//     the relay skips them, and in later batches the claim query skips any
//     row that has an earlier unpublished row with the same key still
//     backing off. Rows with different keys (and rows with no key) are
//     published concurrently, up to Concurrency at a time.
//   - One active relay. Every replica runs a relay, but the claim
//     transaction first takes pg_try_advisory_xact_lock on a key derived
//     from the schema, and a relay that does not get it claims nothing.
//     Without that, a second relay could skip the first relay's locked rows
//     and publish a later row of the same key first. SKIP LOCKED still
//     matters: it keeps claims from blocking on rows locked by anything
//     else. PostgresStoreOptions.Shared drops the lock for services that do
//     not need per-key order across replicas and want more throughput.
//   - Backoff. A failed row gets attempts+1 and waits
//     min(MaxBackoff, InitialBackoff * 2^(attempts-1)), jittered to between
//     half and all of that. Rows are never given up on: a row that keeps
//     failing shows up in skein.outbox.publish_failures and in
//     last_error, and blocks only its own key.
//   - Times come from the database clock (now()), so relays on hosts with
//     skewed clocks agree on what is due.
//
// # Crashes
//
// A crash, or a lost connection, between publishing and committing rolls
// the claim back: those rows are published again by the next claim. That
// is a duplicate, never a loss, and consumers absorb it. When Run's
// context is cancelled mid-batch, the relay stops publishing but still
// marks what it already published (on a context without the
// cancellation), to keep duplicates rare on ordinary shutdowns. Tests drive
// the relay against an in-memory store with injected publish failures,
// commit failures and crashes after publishing, and check that every event
// is published at least once and each key's first publications are in
// order.
//
// # Consuming
//
// ProcessOnce inserts (consumer, event_id) into processed_events in the
// consumer's transaction and reports whether it was new. Consumer.HandleOnce
// wraps that and the effect in one transaction, and Consumer.Handler adapts
// it to events.Handler: a duplicate delivery changes nothing and acks.
//
// # Metrics
//
//	skein.outbox.published          counter, by messaging.destination.name
//	skein.outbox.publish_failures   counter, by messaging.destination.name
//	skein.outbox.publish_lag        histogram, seconds from enqueue to publish
//	skein.events.duplicates         counter, by consumer
//
// # Porting notes
//
// Rust: the same SQL through `sqlx`, the relay as a Tokio task with a
// `Notify` for wakeups and a `JoinSet` for the per-key publish groups.
package outbox
