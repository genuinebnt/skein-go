// Package redisx opens traced Redis clients and provides a typed cache with
// expiry, per-service key namespaces, stampede protection and tag-based
// invalidation.
//
// # Client
//
// Open parses a redis:// URL, adds redisotel tracing and connection-pool
// metrics, and pings, so a bad URL fails at startup.
//
// # Cache
//
// Cache[V] stores values of one type under one name, on a Store:
//
//   - RedisStore, shared by a service's replicas. Every key lives under the
//     service's namespace ("search:c:<cache>:<key>", "search:t:<tag>"), so
//     services sharing a Redis never collide and one service's keys can be
//     listed or dropped by prefix.
//   - MemoryStore, the same semantics in process, for unit tests and for
//     anything that does not need sharing.
//
// GetOrLoad returns the cached value or calls the loader once, however many
// callers miss the same key at the same time (singleflight). The loader
// runs on a context detached from the first caller's cancellation and
// bounded by LoadTimeout, so one impatient caller cannot fail the load for
// everyone waiting on it; each caller still stops waiting when its own
// context ends.
//
// # Tags
//
// Set can attach tags to an entry. InvalidateTags drops every entry with a
// tag, in every cache on the same store: tag an entry with the note it was
// computed from ("note:<id>") and a consumer of documents.events calls
// InvalidateTags(ctx, "note:<id>") when that note changes. An entry carries
// exactly the tags of its latest Set. In Redis an entry is a hash holding
// the value and its tags, and a tag is a set of entry keys; Set and
// invalidation are Lua scripts, so each is atomic, and invalidation checks
// that each member still carries the tag before deleting it. A tag set
// lives as long as its longest-lived member. RedisStore and MemoryStore are
// tested against each other with random operations.
//
// Known limit: a load that started before an invalidation can write its
// (now stale) result after it. The entry's TTL bounds how long that lasts;
// keep TTLs short for data that changes often.
//
// # Failure policy
//
// The cache is an optimisation, never a dependency: if the store fails on
// a read, GetOrLoad loads from the source; if it fails on a write, the
// loaded value is still returned. Both count as skein.cache.errors.
// Invalidation errors are returned, because a consumer must retry them or
// serve stale data.
//
// # Metrics
//
//	skein.cache.hits           counter, by cache.name
//	skein.cache.misses         counter, by cache.name
//	skein.cache.invalidations  counter of entries removed, by cache.name
//	                           (keys) or cache.name="*" (tags)
//	skein.cache.errors         counter, by cache.name and operation
//
// # Porting notes
//
// Rust: `redis-rs` or `fred` with the same Lua scripts; `moka` for the
// memory store (it has TTL but not tags, so keep a tag index beside it);
// singleflight becomes a `DashMap<String, Shared<BoxFuture>>` or
// `async_singleflight`.
package redisx
