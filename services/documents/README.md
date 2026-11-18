# documents

The notes service: notes and their trash, the CRDT op log and snapshots,
live sync, version history, diff, blame, undo and redo, and the
`documents.events` it publishes through its outbox. Contract:
[`proto/skein/v1/documents.proto`](../../proto/skein/v1/documents.proto),
[`docs/api/documents.md`](../../docs/api/documents.md). Decisions:
ADR-0004, 0005, 0006, 0008, 0009.

## Layout

```
cmd/documents/            wiring: config, adapters, gRPC and admin servers, shutdown
cmd/documents-seed/       loads testdata/seed into Postgres and verifies it
internal/domain/          notes, batches, change groups, versions, blame, diff, undo inverse, events
internal/ports/           the interfaces the use cases need (store, identity, Redis, clock)
internal/app/             use cases; one actor per open note (see its doc.go)
internal/adapters/
  grpcapi/                DocumentService, including the Sync stream
  postgres/               op log, snapshots, projection state, outbox (migrations/)
  redis/                  note metadata cache, Authorize cache, presence
  identity/               Authorize, ListReadableNotes, RegisterReplica, RegisterNote over gRPC
  pubsub/                 identity.events consumer (AccessChanged)
  eventcodec/             domain events <-> skein.v1.Event
  admin/                  /healthz, /readyz
internal/fakes/           in-memory ports for tests
internal/seed/            the seed loader, its time-aware policy and verification
migrations/               the documents schema
```

The block model (markers, attributes, inline Markdown, Markdown import and
export) is the pure library [`libs/blocks`](../../libs/blocks), specified in
[`docs/spec/blocks.md`](../../docs/spec/blocks.md).

## How it works

- **Text.** A note is one Fugue sequence (`libs/crdt`) whose blocks start
  with a U+001E marker (wire format v1 has no marker op). Block type, indent,
  checked and code language are last-writer-wins registers beside the
  sequence (`libs/blocks`). Markdown is the exchange format: `CreateNote`
  imports it, `GetNote` and `GetTextAt` export it.
- **Op log.** Append-only `op_batches`: one row per batch of consecutive ops
  from one replica, with who sent it, when and why (edit, suggestion,
  import, undo, redo, attributes). The note is the replay; snapshots are
  checkpoints. See `internal/app/doc.go` for ordering and crash safety.
- **Server operations.** Markdown import, undo and redo generate operations
  on the reserved replica 2 (`domain.ServerReplica`), recording the user
  they were made for, so client counters never collide with the server's.
  Client batches from replica 2 are refused.
- **Undo and redo** are per replica and derived from the op log, so they
  survive restarts. Edits within `UndoWindow` (2 s) of each other form one
  group; each `ApplyOps` call from an agent run is one suggestion group, so
  a user rejects a suggestion by undoing the agent's replica (which the
  user owns). The inverse of an insert is a delete; the inverse of a delete
  reinserts a **copy** (see Limitations).
- **History.** `ListVersions` groups batches by time (`VersionWindow`, 5 min)
  with their authors; each version vector is a prefix of the causal log.
  `GetTextAt` replays the ops a version vector includes (refusing
  inconsistent cuts) with the attributes written up to their largest
  Lamport time. `Diff` diffs by line (patience), then by code point inside
  changed lines (`libs/dp`). `Blame` maps every code point of the Markdown
  to the batch that inserted it (markup belongs to its block's marker).
- **Events** (`documents.events`, ordering key = note ID): NoteCreated,
  NoteRenamed, NoteTrashed, NoteRestored and NotePurged in the transaction
  of their change; BlocksChanged (current text of changed blocks and removed
  block IDs) and LinksChanged (complete outbound `[[link]]` set) debounced
  by `DOCUMENTS_DEBOUNCE`, diffed against the projection state last
  published, which is stored with them.
- **Purge.** Notes trashed longer than `DOCUMENTS_TRASH_RETENTION` (30 days)
  are purged hourly: op log, snapshots and projection deleted, a tombstone
  row keeps the ID `NOT_FOUND`, NotePurged tells projections to erase it.

## Caches (Redis)

| What | Why | Invalidation |
|---|---|---|
| Note metadata (`documents-note`, 5 min) | read by every RPC's access path and GetNote; changes rarely | deleted on rename, trash, restore, purge |
| Authorize grants (`documents-authorize`, 10 s; denials are never cached, since identity denies notes its projection has not seen yet) | every sync batch is checked; identity would see one call per keystroke batch | tag `user:<id>` dropped on AccessChanged, on every instance (shared cache) |
| Presence (hash per note, 30 s TTL) | cursors outlive actor eviction and reach newcomers; no table needed | expiry; cleared when a session closes |

Open sync sessions are re-checked every 30 s, which catches revocations
whose event another instance consumed.

## Running

```
go test -race ./...                                    # unit tests, no Docker
SKEIN_TEST_DATABASE_URL=postgres://... \
  go test -tags integration ./internal/adapters/postgres   # in an existing database
SKEIN_TEST_POSTGRES_DSN=postgres://... \
  go test -tags integration ./internal/adapters/postgres   # a fresh database per test on an existing server
go test -tags integration ./internal/adapters/postgres     # or with testcontainers (Docker)
go run ./cmd/documents-seed -dir ../../testdata/seed/small # needs DATABASE_URL
```

Environment variables are listed in `cmd/documents/main.go`. Compose:
`deploy/compose/services/documents.yml` (gRPC on 127.0.0.1:9102, admin on
127.0.0.1:8102; `docker compose run --rm documents-seed` loads the seed).

## Limitations and follow-ups

- **No restore operation** in the CRDT yet (crdt-sequence.md section 10).
  Undoing a delete reinserts copies with new IDs: concurrent inserts made
  inside the deleted text stay beside the tombstones, anchors (cursors,
  comments, links to elements) do not move to the copies, and blame credits
  the copies to the user who undid. Copied block markers do get the
  original block's attributes. Follow-up: a restore op in libs/crdt.
- **No attribute writes on the wire.** Clients cannot change block types
  yet; attributes come from Markdown import and the seed. Follow-up: a
  marker op and attribute writes in wire format v2 (spec section 10), or an
  `attrs` field on `SyncRequest`/`ApplyOpsRequest` carrying the attribute
  batch format of docs/spec/blocks.md.
- **Element order** is recovered by a second replay (`blocks.Elements`)
  because `crdt.Sequence` has no element iterator; the layout is cached per
  state. Follow-up: an iterator in libs/crdt.
- Comments belong to documents (architecture.md) but have no contract yet.
