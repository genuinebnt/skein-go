// Package app holds the search service's use cases and the ports they
// need: SearchService (Searcher), GraphService (Grapher, Analyzer), the
// event consumers (Projector) and the in-memory projection that follows
// the change feed (Index).
//
// # Data flow
//
//	documents.events ─▶ Projector ─▶ Store.Apply (note states + change feed, one tx)
//	identity.events  ─▶ Projector ─▶ cache tag invalidation, Store.Apply (processed record)
//	change feed      ─▶ Index.CatchUp (every replica) ─▶ domain.Projection
//	RPC              ─▶ Access (readable notes, cached) ─▶ Searcher / Grapher ─▶ Projection (read lock)
//
// # Permissions
//
// Every use case starts from the caller's readable notes (Access, from
// identity's ListReadableNotes) and hands them to the domain, which
// intersects them with candidates before reading, ranking or counting.
// Notes the caller cannot read are NOT_FOUND, never PERMISSION_DENIED.
//
// # Caching
//
//   - Readable notes: Redis, 30 s, tagged by user and workspace, dropped
//     by AccessChanged.
//   - Search pages: Redis, 20 s, keyed by user, workspace version and
//     request, tagged by user, workspace and every note shown, dropped
//     when one of those notes changes or is purged.
//   - Graph analyses: in process, per readable graph and graph version;
//     betweenness and diameter of large graphs computed in the background.
//
// # Porting notes
//
// Goroutines appear only in Index.Run and Analyzer.Run (Tokio tasks); the
// projection is guarded by an RWMutex (tokio::sync::RwLock, never held
// across an await in the read paths, which do no I/O).
package app
