// Package events moves event envelopes between services: Publisher and
// Subscriber interfaces with a Google Cloud Pub/Sub implementation (which
// also drives the local emulator) and an in-memory one for tests.
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
// reserved; Validate rejects them in Attributes.
//
// # Delivery
//
// Delivery is at least once (ADR-0006). A Handler returning nil acks the
// message; returning an error, or panicking, nacks it, and the broker
// redelivers it later. After the subscription's maximum delivery attempts
// (10 in deploy/compose/pubsub-init.sh) Pub/Sub moves it to the topic's
// dead-letter topic, where it waits for a person. Message.DeliveryAttempt
// lets a handler log or alert before that happens. A message that cannot
// even be decoded as an envelope is nacked too, so it reaches the dead
// letters for inspection rather than vanishing.
//
// With an ordering key, a nacked message holds back the later messages
// with the same key until it is acked or dead-lettered; messages with
// other keys keep flowing. Memory implements the same rule, so consumer
// tests see the ordering production has.
//
// Each delivery runs in a consumer span whose parent is the envelope's
// traceparent, named "<subscription> process" with the messaging.*
// semantic-convention attributes; publishing makes a producer span
// "<topic> send".
//
// # Porting notes
//
// Rust: the `google-cloud-pubsub` crate (it honours PUBSUB_EMULATOR_HOST
// too); Memory becomes a `Mutex<HashMap<String, Subscription>>` with the
// same pass-based delivery.
package events
