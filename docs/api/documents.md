# Documents

`DocumentService` ([`documents.proto`](../../proto/skein/v1/documents.proto)):
notes, CRDT operations, history, diff, blame, undo and redo
([ADR-0005](../adr/0005-sequence-crdt.md), [ADR-0006](../adr/0006-op-log-outbox-projections.md)).
Live editing is the `/v1/notes/{noteId}/sync` WebSocket, described in
[websocket.md](websocket.md#documentservicesync). Conventions are in
[README.md](README.md).

| Method | Path | RPC | Needs |
|---|---|---|---|
| `POST` | `/v1/workspaces/{workspaceId}/notes` | CreateNote | create in workspace |
| `GET` | `/v1/notes/{noteId}` | GetNote | read |
| `GET` | `/v1/workspaces/{workspaceId}/notes` | ListNotes | workspace member |
| `DELETE` | `/v1/notes/{noteId}` | DeleteNote | delete |
| `POST` | `/v1/notes/{noteId}:restore` | RestoreNote | delete |
| `POST` | `/v1/notes/{noteId}:rename` | RenameNote | edit |
| `POST` | `/v1/notes/{noteId}/ops` | ApplyOps | edit |
| `GET` | `/v1/notes/{noteId}/versions` | ListVersions | read |
| `POST` | `/v1/notes/{noteId}:textAt` | GetTextAt | read |
| `POST` | `/v1/notes/{noteId}:diff` | Diff | read |
| `GET` | `/v1/notes/{noteId}/blame` | Blame | read |
| `POST` | `/v1/notes/{noteId}:undo` | Undo | edit |
| `POST` | `/v1/notes/{noteId}:redo` | Redo | edit |

A **version vector** identifies a point in a note's history: for each
replica that has edited the note, the highest operation counter included.
Entries are sorted by `replicaId`, and both numbers are JSON strings.

## Notes

### CreateNote

`POST /v1/workspaces/{workspaceId}/notes`. `markdown` is optional initial
content. Send an `Idempotency-Key`.

```json skein.v1.CreateNoteRequest
{
  "title": "Raft log compaction",
  "markdown": "# Raft log compaction\nSnapshots bound the log."
}
```

```json skein.v1.CreateNoteResponse
{
  "note": {
    "id": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
    "workspaceId": "01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f",
    "title": "Raft log compaction",
    "createdBy": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
    "createdAt": "2026-11-16T09:10:00Z",
    "updatedAt": "2026-11-16T09:10:00Z"
  }
}
```

### GetNote

`GET /v1/notes/{noteId}`. With `includeMarkdown=true` the response carries
the current text and the version it reflects, so a client can start syncing
from there.

```
GET /v1/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f?includeMarkdown=true
```

```json skein.v1.GetNoteResponse
{
  "note": {
    "id": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
    "workspaceId": "01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f",
    "title": "Raft log compaction",
    "createdBy": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
    "createdAt": "2026-11-16T09:10:00Z",
    "updatedAt": "2026-11-16T10:02:00Z"
  },
  "markdown": "# Raft log compaction\nSnapshots bound the log and let slow followers catch up.",
  "version": {
    "entries": [
      {"replicaId": "4831727201934127107", "counter": "42"},
      {"replicaId": "9120384756102938475", "counter": "17"}
    ]
  }
}
```

### ListNotes

`GET /v1/workspaces/{workspaceId}/notes`. Only notes the caller can read.
Paginated; `includeTrashed=true` adds trashed notes.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/notes?page.pageSize=50&includeTrashed=true
```

```json skein.v1.ListNotesResponse
{
  "notes": [
    {
      "id": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
      "workspaceId": "01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f",
      "title": "Raft log compaction",
      "createdBy": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
      "createdAt": "2026-11-16T09:10:00Z",
      "updatedAt": "2026-11-16T10:02:00Z"
    },
    {
      "id": "01936f41-3c4d-7e5f-8a61-7b8c9d0e1f20",
      "workspaceId": "01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f",
      "title": "Old draft",
      "createdBy": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
      "createdAt": "2026-11-15T18:00:00Z",
      "updatedAt": "2026-11-15T18:20:00Z",
      "trashed": true
    }
  ],
  "page": {}
}
```

### DeleteNote

`DELETE /v1/notes/{noteId}`. Moves the note to the trash; it stays
restorable until purged. Deleting a trashed note succeeds.

```
DELETE /v1/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f
```

```json skein.v1.DeleteNoteResponse
{}
```

### RestoreNote

`POST /v1/notes/{noteId}:restore`. Takes a note out of the trash. A purged
note is `NOT_FOUND`.

```json skein.v1.RestoreNoteRequest
{}
```

```json skein.v1.RestoreNoteResponse
{}
```

### RenameNote

`POST /v1/notes/{noteId}:rename`. Titles are not part of the CRDT text, so a
rename is last-writer-wins.

```json skein.v1.RenameNoteRequest
{"title": "Raft snapshots"}
```

```json skein.v1.RenameNoteResponse
{}
```

### ApplyOps

`POST /v1/notes/{noteId}/ops`. Applies a batch of CRDT operations without a
sync connection, for imports and scripts. `ops` is base64 of a batch in the
wire format of [`docs/spec/crdt-sequence.md`](../spec/crdt-sequence.md);
`replicaId` must be registered to the caller (`POST /v1/replicas`).
Operations are idempotent, so retrying is safe without an idempotency key.
The response is the note's version after the batch.

```json skein.v1.ApplyOpsRequest
{"replicaId": "4831727201934127107", "ops": "AQIDBAUGBwgJCgsMDQ4PEA=="}
```

```json skein.v1.ApplyOpsResponse
{
  "version": {
    "entries": [
      {"replicaId": "4831727201934127107", "counter": "45"},
      {"replicaId": "9120384756102938475", "counter": "17"}
    ]
  }
}
```

## History

### ListVersions

`GET /v1/notes/{noteId}/versions`. Points in history, newest first, each
grouping edits by author and time. Paginated.

```
GET /v1/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f/versions?page.pageSize=20
```

```json skein.v1.ListVersionsResponse
{
  "versions": [
    {
      "version": {
        "entries": [
          {"replicaId": "4831727201934127107", "counter": "42"},
          {"replicaId": "9120384756102938475", "counter": "17"}
        ]
      },
      "authorIds": ["01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90", "01936f3f-9d5b-7c3f-8a21-4e6b7c8d9ea1"],
      "at": "2026-11-16T10:02:00Z"
    },
    {
      "version": {
        "entries": [
          {"replicaId": "4831727201934127107", "counter": "30"}
        ]
      },
      "authorIds": ["01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90"],
      "at": "2026-11-16T09:12:00Z"
    }
  ],
  "page": {"nextPageToken": "CgwIgICAgICAgICAARIA"}
}
```

### GetTextAt

`POST /v1/notes/{noteId}:textAt`. The note's Markdown at a version. A POST
because a version vector does not fit a query string; it changes nothing.

```json skein.v1.GetTextAtRequest
{
  "version": {
    "entries": [
      {"replicaId": "4831727201934127107", "counter": "30"}
    ]
  }
}
```

```json skein.v1.GetTextAtResponse
{"markdown": "# Raft log compaction\nSnapshots bound the log."}
```

### Diff

`POST /v1/notes/{noteId}:diff`. Text changes from one version to another,
as hunks that concatenate to the old text (equal and delete) and to the new
text (equal and insert).

```json skein.v1.DiffRequest
{
  "from": {
    "entries": [
      {"replicaId": "4831727201934127107", "counter": "30"}
    ]
  },
  "to": {
    "entries": [
      {"replicaId": "4831727201934127107", "counter": "42"},
      {"replicaId": "9120384756102938475", "counter": "17"}
    ]
  }
}
```

```json skein.v1.DiffResponse
{
  "hunks": [
    {"kind": "KIND_EQUAL", "text": "# Raft log compaction\nSnapshots bound the log"},
    {"kind": "KIND_INSERT", "text": " and let slow followers catch up"},
    {"kind": "KIND_EQUAL", "text": "."}
  ]
}
```

### Blame

`GET /v1/notes/{noteId}/blame`. Who wrote each span of the current text.
Spans are half-open code-point ranges that cover the text in order.
`agentRunId` marks text the agent wrote for the user.

```
GET /v1/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f/blame
```

```json skein.v1.BlameResponse
{
  "spans": [
    {
      "end": 45,
      "userId": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
      "at": "2026-11-16T09:12:00Z"
    },
    {
      "start": 45,
      "end": 77,
      "userId": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
      "agentRunId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
      "at": "2026-11-16T10:02:00Z"
    },
    {
      "start": 77,
      "end": 78,
      "userId": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
      "at": "2026-11-16T09:12:00Z"
    }
  ]
}
```

## Undo and redo

### Undo

`POST /v1/notes/{noteId}:undo`. Undoes the last change group made from
`replicaId`, leaving other people's edits alone. The server generates the
inverse operations, applies them, and returns them so a client without a
sync connection can apply them locally. Each call undoes one more group, so
send an `Idempotency-Key` when retrying. Nothing to undo is
`FAILED_PRECONDITION`.

```json skein.v1.UndoRequest
{"replicaId": "4831727201934127107"}
```

```json skein.v1.UndoResponse
{"ops": "EBEQExQVFhcYGRobHB0eHw=="}
```

### Redo

`POST /v1/notes/{noteId}:redo`. Re-applies the change group most recently
undone from `replicaId`. A new edit from that replica clears the redo stack.

```json skein.v1.RedoRequest
{"replicaId": "4831727201934127107"}
```

```json skein.v1.RedoResponse
{"ops": "ICEiIyQlJicoKSorLC0uLw=="}
```
