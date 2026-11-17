# Slice 2: create a note and edit it live

Two editors open the same note, type concurrently (one of them offline for
a while), and converge to the same text through the CRDT. A viewer's edit
is refused. An edit sent just before a socket closes is still applied. This
slice ports the CRDT engine, the block layer and the documents service's
core (one actor per note, the op log), and the gateway's sync bridge if you
want the edge in Rust too.

## The path of an edit

```
browser replica (libs/crdt via WASM)
  types "x" -> local op -> encoded batch (spec/crdt-sequence.md)
  WebSocket GET /v1/notes/{noteId}/sync, subprotocols skein.v1.proto + bearer.<token>
gateway
  wsapi.Handlers.upgrade: verify token, spend api budget, identity read check
  backends.SyncRouter.Route: rendezvous owner of noteId among documents instances
  socket reader: frame -> SyncRequest{ops} (sync budget per frame) -> DocumentService.Sync stream
documents (the owner instance)
  grpcapi.Server.Sync -> app.Service.OpenSession -> Session.Submit
  actor (one task per note): Authorize EDIT (cached) -> replica belongs to caller
    -> crdt.CausalBuffer.Deliver -> blocks.Document applies ops
    -> Store.AppendBatches (op_batches, one transaction)
    -> broadcast SyncResponse{ops} to the other sessions
    -> later (debounce): BlocksChanged / LinksChanged via the outbox
gateway writer -> WebSocket frame -> other browser replica
```

## Contracts and storage

- Protos: `documents.proto` (`CreateNote`, `GetNote`, `ApplyOps`, `Sync`,
  `SyncRequest`, `SyncResponse`, `SyncHello`, `SyncRejected`, `Presence`,
  `Note`), `identity.proto` (`Authorize`, `RegisterNote`,
  `RegisterReplica`, `CreateWorkspace`), `events.proto` (`NoteCreated`,
  `BlocksChanged`, `LinksChanged`), `common.proto`.
- Formats: `docs/spec/crdt-sequence.md` (the op and batch codec),
  `docs/spec/blocks.md` (block markers and attributes).
- WebSocket framing: `docs/api/websocket.md`, "DocumentService.Sync".
- Migrations: `services/documents/migrations/0001_documents.sql` (`notes`,
  `op_batches`, `snapshots`, `projections`, `outbox`); identity's
  `replicas` and `notes` tables (identity stays Go or uses slice 1's port).

## Files to port, in order

Prerequisites: slice 1's prerequisites (`skein-proto`, the platform
crates), plus `platform/redisx` (`client.go`, `store.go`, `cache.go`),
`platform/events` (`events.go`, `memory.go`, `pubsub.go`) and
`platform/outbox` (`outbox.go`, `postgres.go`, `relay.go`, `consumer.go`).

Libraries (guides: [crdt](../../../libs/crdt/PORTING.md),
[blocks](../../../libs/blocks/PORTING.md)):

| # | Go file | Job in this slice |
|---|---|---|
| 1 | `libs/crdt/id.go`, `op.go`, `version.go`, `errors.go` | IDs, ops, version vectors |
| 2 | `libs/crdt/sequence.go`, `invariants.go` | the Fugue sequence and its invariants |
| 3 | `libs/crdt/causal.go` | the causal buffer in front of the sequence |
| 4 | `libs/crdt/codec.go` | the wire format (golden bytes) |
| 5 | `libs/blocks/block.go`, `attrs.go`, `codec.go`, `elements.go`, `errors.go` | block markers and attribute registers over the sequence |
| 6 | `libs/blocks/inline.go`, `markdown.go`, `extract.go` | Markdown import for `CreateNote`; links and mentions for `LinksChanged` |

Identity (Go is fine; if Rust, add to slice 1): `app/authorize.go`
(`Authorize`, `RegisterNote`), `app/workspaces.go` (`CreateWorkspace`,
`RegisterReplica`), `domain/access.go`, `domain/policy.go`,
`domain/roles.go`, and their gRPC methods.

Documents ([services/documents/PORTING.md](../../../services/documents/PORTING.md)):

| # | Go file | What this slice needs from it |
|---|---|---|
| 7 | `internal/domain/note.go`, `errors.go` | notes, subjects, `CheckEditable` |
| 8 | `internal/domain/batch.go` | `Batch`, `Validate`, `Sender`, `SplitRuns`, `Snapshot`, `Projection` |
| 9 | `internal/domain/events.go` | `Event`, `ChangedBlock`, `NextProjection` |
| 10 | `internal/ports/ports.go` | every port |
| 11 | `internal/app/state.go` | `noteState`, `loadNoteState` |
| 12 | `internal/app/actor.go` | the actor and the registry |
| 13 | `internal/app/service.go` | `CreateNote` (with import and `registerNote`), `GetNote`, `ApplyOps`, `access`, `ensureReplica` |
| 14 | `internal/app/session.go` | `Session`, `Frame`, `Submit`, rejections |
| 15 | `internal/app/history.go` | `OpenSession` only |
| 16 | `internal/app/lifecycle.go` | `Run` (debounce, idle eviction), `FlushAll` |
| 17 | `internal/fakes/store.go`, `ports.go` | for the app tests |
| 18 | `internal/adapters/postgres/store.go` | `InsertNote`, `GetNote`, `AppendBatches`, `LoadBatches`, `SaveSnapshot`, `LatestSnapshot`, `LoadProjection`, `PublishProjection` |
| 19 | `internal/adapters/eventcodec/codec.go` | `Encode` |
| 20 | `internal/adapters/identity/client.go` | `Authorize`, `RegisterNote`, `EnsureReplica` |
| 21 | `internal/adapters/redis/redis.go` | `AuthCache`, `NoteCache` (or no-op stubs, see below) |
| 22 | `internal/adapters/grpcapi/server.go` | `CreateNote`, `GetNote`, `ApplyOps`, `Sync`, `sendFrames`, `receiveFrames` |
| 23 | `internal/adapters/admin/admin.go`, `cmd/documents/main.go` | wiring |
| 24 | `internal/seed/*.go`, `cmd/documents-seed/main.go` | for `TestSeedNotesSyncToTheirTruthTextAndVersion` |

Gateway (optional in this slice; [services/gateway/PORTING.md](../../../services/gateway/PORTING.md)):
`internal/domain/rendezvous.go` (with the golden file), `closecode.go`,
`policy.go`, `limits.go`; `internal/app/sockets.go`, `throttle.go`;
`internal/adapters/backends/syncrouter.go`, `backends.go`;
`internal/adapters/wsapi/socket.go`, `handlers.go` (`upgrade`,
`serveSync`, `drainSync`, `syncFrames`); the REST route for `CreateNote`.

## Tests that prove it

Libraries: every conformance fixture in `testdata/conformance/crdt` and
`blocks`, the codec golden bytes, the convergence property tests, and the
Go-to-Rust replay check ([porting-to-rust.md](../../porting-to-rust.md),
section 6).

Documents unit: `TestSplitRunsCutsAtReplicaChanges`,
`TestNextProjectionReportsOnlyDifferences`, `TestNoteLifecycle`,
`TestUnreadableNotesAreNotFoundAndViewersCannotEdit`,
`TestReplicaOfAnotherUserIsRefused`,
`TestApplyOpsIsIdempotentAndHoldsOutOfOrderOps`,
`TestSyncConvergesWithManyUnreliableClients`,
`TestCrashAfterCommitReloadsAcknowledgedStateAndRepublishes`,
`TestFailedAppendAcknowledgesNothingAndReloads`,
`TestSnapshotsBoundReplayAndReloadIdentically`,
`TestIdleActorsAreEvictedAndReloadOnDemand`,
`TestCreateNoteImportsMarkdownBlocks`,
`TestCreatorCanReadANewNoteBeforeIdentityHearsTheEvent`,
`TestSyncStreamsOpsBetweenClientsAndRejectsAViewer`.

Integration: `TestStoreRoundTripsTheOpLogAndRefusesASecondWriter`,
`TestSmallSeedLoadsIntoPostgres`; seed: `TestSmallSeedLoadsToItsCRDTTruth`.

Gateway (if ported): `TestRendezvousMatchesGoldenValues`,
`TestSyncBridgesFramesInBothEncodingsToTheNotesOwner`,
`TestSyncRejectsAFirstFrameThatIsNotAHelloForThePathsNote`,
`TestSlowClientIsDroppedWith1013`,
`TestSyncOpsSentJustBeforeTheClientClosesStillReachDocuments`.

End to end (`e2e/sync_test.go`, `e2e/seed_test.go`):
`TestTwoEditorsConvergeThroughConcurrentTypingAndAnOfflineSpell`,
`TestAViewersEditIsRejectedAndNeverApplied`,
`TestAnEditSentJustBeforeClosingTheSocketIsKept`,
`TestSeedNotesSyncToTheirTruthTextAndVersion`. The e2e clients are real
`libs/crdt` replicas, so these also check that the Rust service's ops decode
in Go.

## Stubbed in this slice

- `ListNotes`, `DeleteNote`, `RestoreNote`, `RenameNote`, `ListVersions`,
  `GetTextAt`, `Diff`, `Blame`, `Undo`, `Redo`: `UNIMPLEMENTED`
  (`ListNotes` is needed by the web app but not by these journeys).
- `domain/history.go`, `domain/undo.go`, trash and purge in `lifecycle.go`.
- Presence: an in-memory `PresenceStore`; the Redis one comes later.
- Caches: a `NoopAuthCache` that always asks identity, and a `NoopNoteCache`.
- The `identity.events` consumer (`AccessChanged`): comes with
  [slice 5](05-share-and-rbac.md); without it, a removed member's socket is
  only closed by the gateway, and every batch is still re-checked.
- Events: keep the outbox and relay (search, ml and notifications consume
  `BlocksChanged`); if you stub them, slices 3, 4 and 6 will not see edits.

## Build

```
cd rust
cargo test -p skein-crdt -p skein-blocks
cargo test -p documents-core
cargo build -p documents && cargo test -p documents --features integration
```
