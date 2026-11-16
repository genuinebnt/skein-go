// Package app holds the gateway's use cases and the ports they need:
//
//   - Throttler spends a request's rate-limit budget for its IP and its
//     user, and fails open when the limiter fails.
//   - Idempotency reserves, completes, replays and releases idempotency
//     records through an IdempotencyStore.
//   - Registry tracks live WebSockets so they can be closed by access
//     revocation, by a change of the note's owning documents instance, or
//     by drain.
//   - Revocations turns identity AccessChanged events into socket closes
//     on every gateway replica, through a RevocationBus.
//
// # Failure policy
//
// Rate limiting and idempotency both fail open when their store fails: the
// request proceeds, the failure is logged at WARN and counted (the
// limiter's decision counter with error.type set, the idempotency outcome
// counter with outcome "error"). They protect the backends and smooth over
// retries; neither is worth an outage of the whole API when Redis is down
// (docs/platform.md, "Rate limits and caches fail open"). Authentication
// is not in this package and fails closed.
//
// Revocations are applied locally before they are broadcast, so a Redis
// outage still closes the sockets on the replica that consumed the event.
// Redis pub/sub is at most once; a replica that misses a broadcast leaves
// a socket open, but documents re-checks every batch of operations
// (ADR-0008), so nothing is accepted from it.
//
// # Invariants
//
// Registry.CheckInvariants: every socket has a kind and a user, sync
// sockets name their note, and once draining has started the registry only
// shrinks.
//
// # Porting notes
//
// Rust: ports become traits held as Arc<dyn Trait>; Registry is a
// Mutex<HashMap<u64, Entry>> plus a tokio::sync::Notify for drain; the
// metric counters are opentelemetry Counter<u64>.
package app
