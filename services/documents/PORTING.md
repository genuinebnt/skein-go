# Porting `services/documents` to Rust

Documents owns notes and their content: the CRDT op log (the source of truth
for every note's text), snapshots, live sync streams, version history, text
at a version, diff, blame, undo and redo, and the trash. It publishes the
document events every projection (search, ml, notifications, identity) is
built from. It is the service that turns `libs/crdt` and `libs/blocks` into
a product, and the one with real concurrency: one actor per open note.

Read the overall plan in [docs/porting/README.md](../../docs/porting/README.md),
[platform/PORTING.md](../../platform/PORTING.md) and the library guides for
[crdt](../../libs/crdt/PORTING.md), [blocks](../../libs/blocks/PORTING.md)
and [dp](../../libs/dp/PORTING.md) first. Slice
[2 (create a note and edit it live)](../../docs/porting/slices/02-live-note-editing.md)
walks through this service end to end.

## Contract

### RPCs (`proto/skein/v1/documents.proto`, `docs/api/documents.md`)

| RPC | REST route | Use case (`internal/app`) |
|---|---|---|
| `CreateNote` | `POST /v1/workspaces/{workspace_id}/notes` | `Service.CreateNote` (optional Markdown import), `service.go` |
| `GetNote` | `GET /v1/notes/{note_id}` | `Service.GetNote` |
| `ListNotes` | `GET /v1/workspaces/{workspace_id}/notes` | `Service.ListNotes` (readable notes, paginated) |
| `DeleteNote` | `DELETE /v1/notes/{note_id}` | `Service.DeleteNote` (to the trash) |
| `RestoreNote` | `POST /v1/notes/{note_id}:restore` | `Service.RestoreNote` |
| `RenameNote` | `POST /v1/notes/{note_id}:rename` | `Service.RenameNote` |
| `ApplyOps` | `POST /v1/notes/{note_id}/ops` | `Service.ApplyOps` (idempotent; the agent's edit path) |
| `Sync` | WebSocket `GET /v1/notes/{noteId}/sync` through the gateway | `Service.OpenSession` and `Session` (`session.go`) |
| `ListVersions` | `GET /v1/notes/{note_id}/versions` | `Service.ListVersions` (`history.go`) |
| `GetTextAt` | `POST /v1/notes/{note_id}:textAt` | `Service.GetTextAt` |
| `Diff` | `POST /v1/notes/{note_id}:diff` | `Service.Diff` |
| `Blame` | `GET /v1/notes/{note_id}/blame` | `Service.Blame` |
| `Undo` | `POST /v1/notes/{note_id}:undo` | `Service.Undo` |
| `Redo` | `POST /v1/notes/{note_id}:redo` | `Service.Redo` |

The `Sync` frames are `SyncRequest` (`hello`, `ops`, `presence`) and
`SyncResponse` (`ops`, `presence`, `rejected`); the framing and the rules
(the first frame is a hello for the path's note) are in
`docs/api/websocket.md`. A note the caller cannot read is `NOT_FOUND`,
never `PERMISSION_DENIED`.

### Events

| Direction | Topic / subscription | Messages |
|---|---|---|
| publishes | `documents.events` (outbox, ordering key = note ID) | `NoteCreated`, `NoteRenamed`, `NoteTrashed`, `NoteRestored`, `NotePurged`, `BlocksChanged` (debounced, only changed blocks), `LinksChanged` |
| consumes | `documents.identity-events` on `identity.events` | `AccessChanged`: drop cached decisions for the user and re-check open sync sessions |

### Calls to other services

Identity: `Authorize` (cached, `DOCUMENTS_AUTH_CACHE_TTL`),
`ListReadableNotes`, `RegisterNote` (on create, so the creator can read the
note before identity consumes `NoteCreated`), and replica ownership checks
for `RegisterReplica`'d replicas.

### Storage

Schema `documents`, `migrations/0001_documents.sql`: `notes`, `op_batches`
(the op log, primary key per note and batch number), `snapshots`,
`projections` (the state last published to search and ml), `outbox`,
`processed_events`. Redis: note metadata cache, the `Authorize` cache and
presence.

### Environment

`DOCUMENTS_GRPC_ADDR`, `DOCUMENTS_ADMIN_ADDR`, `IDENTITY_GRPC_TARGET`,
`IDENTITY_JWKS_URL`, `DOCUMENTS_AUTH_CACHE_TTL` (10 s),
`DOCUMENTS_NOTE_CACHE_TTL` (5 min), `DOCUMENTS_IDLE_TIMEOUT` (5 min),
`DOCUMENTS_DEBOUNCE` (2 s), `DOCUMENTS_SNAPSHOT_EVERY` (5000),
`DOCUMENTS_TRASH_RETENTION` (720 h), plus the platform's. The list with
defaults is the doc comment of `cmd/documents/main.go`.

## Learn first

| Topic | Read | What to take from it |
|---|---|---|
| the CRDT | [Fugue paper (Weidner and Kleppmann)](https://arxiv.org/abs/2305.00583), `docs/spec/crdt-sequence.md`, ADR-0005 | why Fugue does not interleave concurrent typing; the op format the op log stores |
| actors in Tokio | [Actors with Tokio (Alice Ryhl)](https://ryhl.io/blog/actors-with-tokio/) | the task-owns-state plus cloneable-handle pattern; how to shut an actor down when every handle is dropped; exactly the shape of `app/actor.go` |
| channels | [Tokio tutorial: channels](https://tokio.rs/tokio/tutorial/channels) | `mpsc` for the inbox, `oneshot` for each reply, bounded capacity |
| backpressure | [I'm not feeling the async pressure (Armin Ronacher)](https://lucumr.pocoo.org/2020/1/1/async-pressure/) | why a session that falls behind is closed rather than buffered without bound |
| blocking | [Async: what is blocking?](https://ryhl.io/blog/async-what-is-blocking/) | replaying a large op log is CPU work; keep it off the reactor or bound it |
| gRPC streaming in tonic | [Getting started with gRPC-Rust: streaming](https://codelabs.developers.google.com/grpc/getting-started-grpc-rust-streaming) | bidirectional streams: `Streaming<Req>` in, `ReceiverStream` out |
| event sourcing and the outbox | ADR-0006 and ADR-0016, [Transactional outbox](https://microservices.io/patterns/data/transactional-outbox.html) | the op log as event store; why events and state commit together |
| Pub/Sub ordering | [Ordering messages](https://cloud.google.com/pubsub/docs/ordering) | why events use the note ID as ordering key, and what a nack does to later events of that note |
| sqlx | [sqlx](https://docs.rs/sqlx/latest/sqlx/) | `bytea` columns as `Vec<u8>` or `Bytes`, `ON CONFLICT DO NOTHING` for idempotent appends |
| reference code | [tokio-rs/axum examples](https://github.com/tokio-rs/axum) (`examples/chat`, `examples/websockets`) | broadcast fan-out to many connections, which the actor does per note |

## Port order

`internal/domain`, `internal/ports`, `internal/app` and `internal/fakes` go
into `documents-core`; adapters and `cmd/` into `documents`. Go keeps the
ports in their own package so fakes can import them without importing
`app`; in Rust, a `ports` module inside `documents-core` does the same job.

### 1. Domain (`internal/domain`, pure)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `note.go` | `NoteID`, `WorkspaceID`, `Subject` (user, optionally an agent run), `NormalizeTitle`, `Note` with `Rename`, `Trash`, `Restore`, `CheckEditable` | `uuid` | `TestNoteLifecycle` |
| `errors.go` | sentinel errors (not found, trashed, conflict, invalid) | | used everywhere |
| `batch.go` | `Batch` (one replica's ops with consecutive counters, or only attribute writes; who, when, why: `BatchKind`), `Validate`, `Sender`, `SplitRuns`, `Snapshot`, `Projection` | `skein-crdt`, `skein-blocks` | `TestSplitRunsCutsAtReplicaChanges` |
| `events.go` | `EventKind`, `ChangedBlock`, `Event`, `NextProjection` (diff of blocks against the last published state) | `skein-blocks` | `TestNextProjectionReportsOnlyDifferences` |
| `history.go` | `ChangeGroup`, `UndoStacks` (derived from the log alone), `GroupVersions`, `Authorship`, `Blame`, `DiffText` | crdt, blocks, `skein-dp` | `TestUndoStacksGroupEditsByTimeAndReplica`, `TestUndoRedoAndANewEditMoveGroupsBetweenStacks`, `TestGroupVersionsCutsAtTimeGapsAndKeepsCumulativeVectors`, `TestBlameAttributesMarkdownPositionsToBatches`, `TestDiffTextAppliesAndIsCanonical`, `TestDiffTextExampleFromTheAPIDocs` |
| `undo.go` | `Inverse`: the ops that undo one change group, touching only that group's own edits | crdt, blocks | `TestInverseUndoesOnlyTheGroupsOwnEdits`, `TestInverseOfAnInsertLeavesTextOthersDeletedAlone`, `TestInverseCopiesBlockAttributesToReinsertedMarkers` |

`BatchKind` and `EventKind` become enums with data; the Go "fields of
another kind must be zero" checks disappear.

### 2. Ports (`internal/ports`)

| Go file | Job |
|---|---|
| `ports/ports.go` | `Store` (notes, op log, snapshots, projections, purge; event-writing methods enqueue to the outbox in the same transaction), `NoteQuery`, `Action` and `Check`, `Authorizer`, `AuthCache`, `NoteRegistry`, `ReplicaRegistry`, `NoteCache`, `Presence` and `PresenceStore`, `Clock` |

### 3. Application (`internal/app`)

Port in this order; `state.go` and `actor.go` are the core.

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `state.go` | `noteState`: one open note (a `blocks.Document`, the `crdt.CausalBuffer` in front of it, the op log's batch metadata, sessions, the last published projection); `loadNoteState` from the latest snapshot plus later batches; refuses a log that does not replay | domain, crdt, blocks | `TestSnapshotsBoundReplayAndReloadIdentically`, `TestCrashAfterCommitReloadsAcknowledgedStateAndRepublishes` |
| `actor.go` | one task per note owning its `noteState`; the registry routes by note ID, starts actors on first use, evicts idle ones only while nothing is queued; `errActorStopped` (safe to retry) and `poisoned` (memory may be ahead of the database: stop and reload) | state | `TestIdleActorsAreEvictedAndReloadOnDemand`, `TestFailedAppendAcknowledgesNothingAndReloads` |
| `service.go` | `Config`, `Deps`, `Service`; permission checks (`access`, `checkNote`), replica ownership; `CreateNote` (with Markdown import and `RegisterNote`), `GetNote`, `ListNotes`, rename, delete, restore, `ApplyOps`, `ApplyAttrs`, `Inspect` | actor, ports | `TestNoteLifecyclePublishesEventsInOrder`, `TestUnreadableNotesAreNotFoundAndViewersCannotEdit`, `TestReplicaOfAnotherUserIsRefused`, `TestApplyOpsIsIdempotentAndHoldsOutOfOrderOps`, `TestListNotesPaginatesReadableNotesAndRejectsForeignTokens`, `TestCreateNoteImportsMarkdownBlocks`, `TestCreatorCanReadANewNoteBeforeIdentityHearsTheEvent`, `TestCreateNoteSucceedsWhenRegistrationFails` |
| `session.go` | `Session`: one live sync connection; `Frame` kinds; `Submit` checks every batch (not only the connection) and answers refusals with a rejected frame; frames go through a bounded queue the actor never blocks on | actor, service | `TestSyncConvergesWithManyUnreliableClients`, `TestRevokedAccessMidSessionRejectsBatchesThenClosesTheStream`, `TestPresenceReachesOthersAndNewcomers` |
| `history.go` | `ListVersions`, `GetTextAt`, `Diff`, `Blame`, `Undo`, `Redo`, `OpenSession` | domain history and undo, actor | `TestHistoryVersionsTextAtAndDiffAgreeWithWhatWasWritten`, `TestUndoTouchesOnlyTheReplicasOwnEdits`, `TestAgentSuggestionIsUndoneAsOneGroup` |
| `lifecycle.go` | `Run` (background loops), `RecoverProjections` (publish projections left behind by a crash), `FlushAll` on shutdown, `AccessChanged`, `PurgeTrashed` after retention | service | `TestBlocksChangedIsDebouncedAndCarriesOnlyChanges`, `TestPurgeErasesTheNoteAndKeepsItNotFound` |
| `metrics.go` | OpenTelemetry instruments | | |

### 4. Fakes (`internal/fakes`)

| Go file | Job |
|---|---|
| `fakes/store.go` | in-memory `Store` recording the outbox, batches and snapshots, with failure injection (the crash tests use it) |
| `fakes/ports.go` | `Authorizer` with settable roles, `Replicas`, `NoteCache`, `Presence`, a fake clock |

### 5. Adapters

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `migrations/0001_documents.sql` | the schema | `skein-postgres` | migrations apply on the native Postgres |
| `adapters/postgres/store.go` | every `Store` method; `AppendBatches` refuses a second writer through the primary key; events via `outbox.Enqueue`; `Notify` to wake the relay | ports, `skein-outbox` | `TestStoreRoundTripsTheOpLogAndRefusesASecondWriter`, `TestSmallSeedLoadsIntoPostgres` |
| `adapters/eventcodec/codec.go` | encode domain events as `skein.v1.Event` envelopes; decode `AccessChanged` | `skein-proto` | `TestEncodedEventsDecodeAsTheContract`, `TestDecodeAccessChangedIgnoresOtherPayloads` |
| `adapters/pubsub/accesschanged.go` | `identity.events` handler | codec, `skein-events` | `TestAccessChangedReachesTheUseCaseAndOtherMessagesAreAcked` |
| `adapters/identity/client.go` | `Authorizer`, `NoteRegistry`, `ReplicaRegistry` over identity's tonic client | `skein-grpc` | `TestAuthorizeMapsSubjectAndActions`, `TestReplicaOwnedBySomeoneElseIsPermissionDenied` |
| `adapters/redis/redis.go` | `NoteCache`, `AuthCache` (forgets a user on invalidation; never caches a denial), `Presence` with expiry | `skein-redis` | `TestAuthCacheServesRepeatsAndForgetsAUserOnInvalidation`, `TestAuthCacheAsksAgainAfterADenial`, `TestPresenceExpiresAndIsOrderedByReplica` |
| `adapters/grpcapi/server.go` | the tonic service; `Sync` as a bidirectional stream with a sender task (`sendFrames`) and a receiver task (`receiveFrames`) | app | `TestUnaryRPCsMapRequestsAndErrors`, `TestSyncStreamsOpsBetweenClientsAndRejectsAViewer` |
| `adapters/admin/admin.go` | `/healthz`, `/readyz` (first failing check) | | `TestReadyzReportsTheFirstFailingCheck` |

### 6. Seed loader and main

| Go file | Job | Proven by |
|---|---|---|
| `internal/seed/dataset.go`, `loader.go`, `policy.go`, `replicas.go`; `cmd/documents-seed/main.go` | load notes and op batches from `testdata/seed/<profile>` through the store, replaying every batch through the CRDT; the seed's own permission policy for the loader's checks | `TestSmallSeedLoadsToItsCRDTTruth` (final texts and version vectors equal `truth/crdt`), `TestPolicyMatchesThePermissionTruth`, `TestOpenRefusesATamperedDataset` |
| `cmd/documents/main.go` | wiring: config, telemetry, Postgres and migrations, Redis, Pub/Sub, identity client, `Service::run`, outbox relay, consumer, gRPC and admin servers; shutdown flushes actors before the relay stops | the e2e suite |

## Dependencies

- Libraries: `libs/crdt` (sequence, causal buffer, codec), `libs/blocks`
  (block markers, attributes, Markdown import and export, link and mention
  extraction), `libs/dp` (diff).
- Platform: `config`, `logging`, `telemetry`, `auth` (verify), `redisx`,
  `postgres`, `events`, `outbox`, `grpcx`, `shutdown`.
- Generated code: `skein-proto` (documents, events, identity client).
- Other services: identity (Go or Rust).

## Crates

`tokio` (mpsc, oneshot, `JoinSet`, `time`), `tokio-stream`
(`ReceiverStream` for the `Sync` response stream), `tokio-util`
(`CancellationToken`), `tonic`, `prost`, `bytes`, `sqlx`, `redis`, `uuid`,
`chrono`, `thiserror`, `tracing`; tests: `proptest`, `tokio::test` with
`start_paused = true` for debounce and idle-eviction timers.

## Pitfalls

- **The actor is the design.** Port it as Ryhl's pattern: a `NoteHandle`
  holding an `mpsc::Sender<Command>` and the task owning `NoteState`. Prefer
  a `Command` enum with a `oneshot::Sender` reply per variant over boxed
  closures; it is easier to read and to log.
- **The registry race.** An actor may be removed from routing only while
  nothing is queued for it, and a caller that reaches a stopping actor must
  retry on a new one, knowing its request did not run. In Rust, check
  `sender.is_closed()` and the `send` error, and have the reply written
  before the actor drops its receiver. `TestIdleActorsAreEvictedAndReloadOnDemand`
  covers it; make it fail once on purpose.
- **Apply, then append, then broadcast.** A batch is applied in memory,
  appended to the op log, and only after the commit broadcast and
  acknowledged. If the append fails, the actor is poisoned and stops; the
  next request reloads from the store. Do not "recover" in place.
- **Never block the actor on a session.** Use `try_send` into each session's
  bounded queue and close the session when it is full; the client resyncs
  from its version vector.
- **`std::sync::Mutex` in the registry, never across an await.** The
  registry is a `Mutex<HashMap<NoteId, NoteHandle>>`; clone the handle out,
  drop the guard, then send.
- **Debounce timers.** Use `tokio::time::sleep_until` inside the actor's
  `select!` loop rather than a separate timer task per note, and test with
  paused time.
- **Code points, not bytes.** Positions in the API are code points inside
  the service (ADR-0003); UTF-16 conversion happens only at the browser
  edge. `&s[i..j]` on a Rust `String` slices bytes and panics inside a
  character.
- **Snapshots are in the CRDT codec**, so a snapshot written by Go must load
  in Rust and the reverse. Load a Go-written database with the Rust service
  before switching over.
- **One writer per note across processes.** The gateway routes every sync
  stream for a note to one instance (rendezvous hashing); the op log's
  primary key turns a second writer into a conflict. Keep both.

## Done when

- every Go test above passes in Rust, including the randomised convergence
  test with its seed logged;
- the small seed loads and matches `truth/crdt`;
- a database written by Go documents is served correctly by Rust documents;
- `make e2e` passes with Rust documents, notably
  `TestTwoEditorsConvergeThroughConcurrentTypingAndAnOfflineSpell`,
  `TestAViewersEditIsRejectedAndNeverApplied`,
  `TestRemovingAMemberClosesTheirOpenSyncSocket`,
  `TestAnEditSentJustBeforeClosingTheSocketIsKept`,
  `TestVersionHistoryDiffAndUndo` and
  `TestSeedNotesSyncToTheirTruthTextAndVersion`.
