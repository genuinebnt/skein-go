// Package app holds the documents service's use cases: notes CRUD with
// trash, restore and purge; live sync; ApplyOps; history (versions, text at
// a version, diff, blame); undo and redo; and the reaction to identity's
// AccessChanged. It talks to the outside only through the ports in package
// ports, so every use case runs in tests on in-memory fakes.
//
// # One actor per note
//
// Each open note is owned by one goroutine (an actor) holding its
// blocks.Document (the crdt.Sequence plus block attributes), the
// crdt.CausalBuffer in front of it, the op log's batch metadata, the open
// sync sessions and the last published projection. Requests reach it as
// closures through the registry, which routes by note ID, starts an actor on
// first use and evicts it after IdleTimeout without sessions. The gateway
// sends every sync connection of a note to the same instance (consistent
// hashing), so fan-out never crosses instances.
//
// Two actors must never serve one note in one process: an actor is removed
// from routing only while nothing is queued for it, and a caller holding a
// removed actor sees it stopped and retries on a new one; a request is
// reported as not run only if it really did not run (the reply is written
// before the actor closes), so retrying is safe even for Undo. Across
// processes the op log's primary key turns a second writer into a conflict.
//
// # Ordering and crash safety
//
// A batch is applied in memory, then appended to the op log, and only after
// the commit broadcast to other sessions and acknowledged. If the append
// fails the actor is poisoned: it stops without writing anything else, and
// the next request reloads the note from the store, which holds exactly what
// was acknowledged. Operations that are not causally ready wait in the
// buffer and are persisted when they apply; a crash loses them, and clients
// resend after reconnecting (operations are idempotent).
//
// BlocksChanged and LinksChanged are debounced: the actor records when the
// first unpublished change happened and, Debounce later, diffs the current
// blocks against the projection state it last published, then writes the
// new state and the events in one transaction. On load, a projection behind
// the op log (a crash between commit and publication) is published at once,
// and RecoverProjections does the same for notes nobody opens.
//
// Snapshots (every SnapshotEvery operations and on eviction) hold every op
// and attribute write through a batch, in the CRDT codec; loading replays
// the latest snapshot, then the batches after it.
//
// # Permissions
//
// Every use case asks identity's Authorize (through a short cache that
// AccessChanged invalidates per user) for read access plus the action. A
// note the caller cannot read is NOT_FOUND. Sync checks every incoming
// batch, not only the connection, and refuses with a rejected frame, never
// silently. A client replica must belong to the caller (RegisterReplica in
// identity) and every operation in a batch must come from it, so authorship
// cannot be forged.
//
// # Porting notes
//
// The actor is a Tokio task with an mpsc inbox of boxed closures (or a
// command enum); the registry a Mutex<HashMap<NoteId, ActorHandle>>.
// Session frames go through a bounded channel the actor never blocks on: a
// session that falls behind is closed and its client resyncs.
package app
