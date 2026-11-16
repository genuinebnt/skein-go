// Package redisstore implements the gateway's Redis-backed ports:
//
//   - IdempotencyStore keeps one JSON record per caller and key under
//     "<namespace>:idem:<user>:<key>". Reserve is SET NX PX, with a GET when
//     the key exists. Complete and Release are Lua scripts that compare the
//     stored record's owner token before writing or deleting, so a request
//     that outlived its reservation can never overwrite or free a key a
//     later request now holds. A record that does not decode is reported as
//     an error, which the app layer treats as a store failure (fail open).
//   - RevocationBus broadcasts revocations as JSON on the channel
//     "<namespace>:access-revoked". Redis pub/sub reaches every subscribed
//     replica and is at most once, which is enough because documents
//     re-checks permissions on every batch (see app.Revocations).
//
// What is cached here, and why: nothing else. The gateway owns no data;
// the only state it keeps in Redis is rate-limit cells (platform
// ratelimit), idempotency records (24 hours, required by the API) and the
// revocation channel. Authorization decisions are cached by the services
// that make them.
//
// # Porting notes
//
// Rust: the same two scripts with `redis::Script`, and `redis::aio::PubSub`
// for the channel; records stay JSON so Go and Rust replicas can share a
// Redis during a rollout.
package redisstore
