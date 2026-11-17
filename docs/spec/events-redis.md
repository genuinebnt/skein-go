# Spec: events over Redis Streams, version 1

This is the normative description of the Redis events backend
(`EVENTS_BACKEND=redis`, ADR-0022): the keys, the entry format and the
delivery algorithm. The Go implementation is `platform/events` (`redis.go`);
the Python one is `skein_events` (`platform/events/python`). Both, and the
Rust port, must follow this document exactly, because a Go service's events
are consumed by Python services and the other way round. The partition
function is pinned by `testdata/conformance/events/redis-wire.json`.

## 1. Where events live

Database `EVENTS_REDIS_DB` (default 0) of the server named by
`EVENTS_REDIS_URL`, or else `REDIS_URL` (Python services also fall back to
their own `SKEIN_<SERVICE>_REDIS_URL`). The URL's own database number is
ignored: every service must share the streams, while each keeps its cache
in a database of its own.

## 2. Keys

| Key | Type | Holds |
|---|---|---|
| `skein:events:stream:<topic>:<p>` | stream | partition `p` (0 to 7) of a topic |
| `skein:events:lease:<subscription>:<p>` | string, with expiry | the token of the consumer that owns the partition |
| `skein:events:attempts:<subscription>:<p>` | hash | entry ID to delivery attempts so far |
| `skein:events:retry:<subscription>:<p>` | hash | entry ID to the Unix time in ms before which it is not redelivered |
| `skein:events:consumers:<subscription>` | sorted set | consumer name to last heartbeat (Unix ms) |

Each subscription is a consumer group, named as the subscription, on every
partition stream of its topic. Groups are created at service startup with
`XGROUP CREATE <stream> <subscription> 0 MKSTREAM` (an existing group is
left alone), for every subscription in the topology, which mirrors
`deploy/compose/pubsub-init.sh`. A dead-letter topic `<topic>.dead-letter`
is an ordinary topic.

## 3. Partitions

There are exactly 8 partitions per topic. An envelope goes to partition
`FNV-1a-32(bytes) mod 8`, where `bytes` is the UTF-8 ordering key, or the
event ID when the ordering key is empty. FNV-1a-32 starts from `0x811c9dc5`
and, for each byte, XORs it in and multiplies by `0x01000193` modulo 2^32.

## 4. Entries

`XADD <stream> MAXLEN ~ <max> * <fields>`, with fields in this order (a
reader must not depend on the order):

| Field | When | Value |
|---|---|---|
| `id` | always | the event ID (UUIDv7) |
| `source` | always | the publishing service |
| `key` | when set | the ordering key |
| `traceparent` | when set | W3C trace context |
| `attr:<name>` | per attribute, sorted by name | the attribute's value |
| `payload` | always | the encoded `skein.v1.Event`, raw bytes |

The publish time is the millisecond part of the entry ID. An entry without
`id` or `source` is malformed: it is never given to a handler, and it is
retried and dead-lettered like a failing message so a person can inspect
it. Readers ignore fields they do not know.

## 5. Delivery

A consumer of a subscription, every `ack_deadline / 3`:

1. heartbeats (`ZADD` its name with the current time, removes members older
   than the ack deadline) and computes its share,
   `ceil(8 / live consumers)`;
2. renews each lease it holds (compare-and-`PEXPIRE` to the ack deadline);
   a lease it can no longer renew is lost, and its worker stops;
3. gives up leases beyond its share, highest partition first, after the
   worker finishes its current message (compare-and-`DEL`);
4. acquires free partitions up to its share (`SET NX PX ack_deadline` with a
   token unique to this acquisition), and claims every pending entry of
   each one (`XAUTOCLAIM ... 0 0-0 JUSTID` until the cursor is `0-0`).

A worker owns one partition and repeats a **pass**:

1. read the retry hash;
2. read its own pending entries (`XREADGROUP ... <stream> 0`, then from the
   last ID seen, 100 at a time), then, while fewer than 100 entries are
   waiting, new ones (`XREADGROUP ... >`, blocking up to the earliest retry
   time or 1 s when the pass delivered nothing). Pending entries all come
   before new ones in ID order, so the pass sees entries in ID order;
3. for each entry, in order:
   - an entry with no fields was trimmed: acknowledge and forget it;
   - if an earlier entry with the same `key` is waiting in this pass, skip it;
   - if its retry time is in the future, skip it and hold back its key;
   - otherwise `HINCRBY` its attempts (before the handler, so a crash
     counts) and deliver it with that attempt number;
   - ack: in one transaction, `XACK` and `HDEL` it from both hashes;
   - nack, with attempts at the maximum and a dead-letter topic: copy its
     fields unchanged to the dead-letter topic's partition stream, adding
     `attr:CloudPubSubDeadLetterSourceSubscription` and
     `attr:CloudPubSubDeadLetterSourceDeliveryCount`, then acknowledge
     and forget it;
   - nack otherwise: set its retry time to now plus the backoff for the
     attempt (minimum backoff doubled per earlier attempt, capped at the
     maximum: 1 s to 60 s), and hold back its key.

A delivery whose consumer dies is redelivered once that consumer's lease
lapses, after at most the ack deadline (30 s): the visibility timeout.
On a clean stop a consumer finishes its current message, releases its
leases and leaves the heartbeat set, so another consumer takes over at
once.

## 6. Guarantees

At least once. Each key's messages are delivered in publish order, and a
nacked message holds back its key's later messages until it is acked or
dead-lettered; other keys keep flowing. An entry trimmed by `MAXLEN` before
a slow subscription acknowledged it is lost to that subscription (logged
when noticed). Two consumers can deliver the same message only after a
lease is lost while its holder is still running, which at-least-once
delivery allows.
