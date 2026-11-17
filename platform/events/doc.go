// Package events moves event envelopes between services: Publisher and
// Subscriber interfaces with three backends that behave alike (ADR-0022),
// chosen by EVENTS_BACKEND through LoadConfig and Open:
//
//	pubsub  Google Cloud Pub/Sub, or its emulator (PUBSUB_EMULATOR_HOST); the default
//	redis   Redis Streams: no Google tooling at all (docs/local-first.md)
//	memory  in-process, for tests and a service run on its own
//
// A contract suite (contract_test.go) runs the same tests against Memory,
// Redis (miniredis in unit tests, a real server under the integration tag)
// and the Pub/Sub emulator.
//
// # Envelope
//
// An Envelope is the transport form of an event: the payload as bytes
// (an encoded skein.v1.Event from events.proto; this package never decodes
// it, so it does not depend on generated code) plus the fields a broker or
// consumer needs without decoding:
//
//	ID           UUIDv7, the deduplication key (outbox.ProcessOnce uses it)
//	Source       the publishing service
//	OrderingKey  usually the note ID; events with one key arrive in order
//	Traceparent  W3C trace context, so a trace continues across the queue
//	Attributes   anything else, string to string
//
// On Pub/Sub, ID, Source and Traceparent travel as the attributes
// "skein-event-id", "skein-source" and "traceparent", and OrderingKey is
// Pub/Sub's own ordering key. Those names and the "goog" prefix are
// reserved; Validate rejects them in Attributes. Redis carries the same
// fields as stream entry fields (docs/spec/events-redis.md).
//
// # Topology
//
// deploy/compose/pubsub-init.sh is the one list of topics and
// subscriptions: each consumer has its own subscription, each topic a
// dead-letter topic, and every subscription the same policy (10 attempts,
// backoff 1 s doubling to 60 s, 30 s ack deadline). DefaultTopology mirrors
// it for the backends that provision themselves, and a test fails when the
// two differ. Pub/Sub's topology is created by infrastructure; Open
// provisions Redis and Memory at startup (Provisioner).
//
// # Delivery
//
// Delivery is at least once (ADR-0006). A Handler returning nil acks the
// message; returning an error, or panicking, nacks it, and the broker
// redelivers it after the subscription's backoff. After the maximum
// delivery attempts the message goes to the dead-letter topic, where it
// waits for a person. Message.DeliveryAttempt lets a handler log or alert
// before that happens. A message that cannot even be decoded as an
// envelope is nacked too, so it reaches the dead letters for inspection
// rather than vanishing.
//
// Ordering: every backend delivers a key's messages for the first time in
// publish order, and messages with other keys flow independently. After a
// nack, Memory and Redis hold the key's later messages back until the
// nacked one is acked or dead-lettered. The Pub/Sub emulator does not: it
// delivers them during the nacked message's backoff, so a consumer must
// not assume that a nack stops its key (the contract suite asserts only
// what all three share).
//
// Each delivery runs in a consumer span whose parent is the envelope's
// traceparent, named "<subscription> process" with the messaging.*
// semantic-convention attributes; publishing makes a producer span
// "<topic> send".
//
// # Redis backend
//
// Each topic is RedisPartitions (8) streams; an envelope goes to partition
// FNV-1a(ordering key, or event ID when there is none) mod 8, so a key's
// events share one stream and keep their order. Each subscription is a
// consumer group on every partition stream of its topic. Keys are listed
// in docs/spec/events-redis.md; the Python backend (skein_events) uses
// the same ones, so Go and Python services exchange events.
//
// One consumer at a time owns a partition of a subscription, through a
// lease (SET NX PX, renewed every third of the ack deadline). Consumers of
// one subscription heartbeat into a sorted set and each takes at most
// ceil(8 / live consumers) partitions, so replicas share the work. A new
// lease holder claims every pending entry of the partition: the previous
// holder either released the lease after finishing its work or stopped
// renewing it an ack deadline ago. That is the visibility timeout: a
// delivery to a consumer that dies is redelivered once its lease lapses.
//
// A partition worker delivers entries one at a time in entry-ID order:
// first the pending ones (nacked, or claimed from a previous holder), then
// new ones, read with XREADGROUP. It counts attempts in a hash (before
// calling the handler, so a crash counts), and on a nack stores the time
// before which the entry must not be retried (Backoff of the attempt) in a
// second hash and holds back that key for the rest of the pass. An ack
// removes the entry from the group's pending list and both hashes in one
// transaction. After MaxDeliveryAttempts the entry's fields are copied
// unchanged to the dead-letter topic, with Pub/Sub's
// CloudPubSubDeadLetterSource* attributes added.
//
// Invariants (Redis.CheckInvariants, after every test): attempts and retry
// times refer only to pending entries; every retry time has an attempt
// count; with a dead-letter topic, no pending entry has used every attempt.
// Memory.CheckInvariants states its own.
//
// Events live in database EVENTS_REDIS_DB (default 0) of the server in
// EVENTS_REDIS_URL or REDIS_URL, whatever database that URL names: every
// service must share the streams, while services keep their caches in
// different databases.
//
// # Porting notes
//
// Rust: the `google-cloud-pubsub` crate (it honours PUBSUB_EMULATOR_HOST
// too); `redis` with the streams commands for Redis, the same keys and
// fields, and FNV-1a from `fnv`; Memory becomes a
// `Mutex<HashMap<String, Subscription>>` with the same pass-based
// delivery. testdata/conformance/events pins the partition function.
package events
