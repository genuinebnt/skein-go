# WebSocket endpoints

Three RPCs stream, which grpc-gateway cannot serve to a browser. The gateway
serves each as a WebSocket and bridges it to the gRPC stream; they have no
`google.api.http` mapping and are not in the OpenAPI document.

| Endpoint | gRPC | Client sends | Server sends |
|---|---|---|---|
| `GET /v1/notes/{noteId}/sync` | `DocumentService.Sync` (bidirectional) | `SyncRequest` frames | `SyncResponse` frames |
| `GET /v1/chats/{chatId}/messages` | `AgentService.SendMessage` (server streaming, one call per client frame) | `SendMessageRequest` frames | `SendMessageResponse` frames |
| `GET /v1/notifications/live` | `NotificationService.Subscribe` (server streaming) | nothing | `SubscribeResponse` frames |

All three are `wss://` in production, on the same host as the REST API.

## Connecting

### Subprotocols and framing

The client picks the encoding with `Sec-WebSocket-Protocol`; the server
echoes the one it accepted:

| Subprotocol | Frames | Encoding |
|---|---|---|
| `skein.v1.json` (default when none is offered) | text | one message per frame, [proto3 JSON](README.md#json) |
| `skein.v1.proto` | binary | one message per frame, protobuf binary encoding |

Each frame holds exactly one complete message: no length prefix, no batching
across frames, no message split across frames. Frames are at most 1 MiB; a
larger frame closes the connection with `1009`. The sync client should use
`skein.v1.proto`, since operations are bytes and base64 costs a third more;
the JSON encoding exists for debugging and simple clients.

### Authentication

Browsers cannot set an `Authorization` header on a WebSocket, and a token in
the URL would end up in logs. The access token is therefore offered as an
extra subprotocol entry, `bearer.<access token>`, after the real one:

```
GET /v1/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f/sync HTTP/1.1
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Protocol: skein.v1.proto, bearer.EXAMPLE_ACCESS_TOKEN
X-Request-Id: 01936f80-6f70-7a81-9ba3-1e2f30415263
```

```js
new WebSocket(url, ["skein.v1.proto", `bearer.${accessToken}`]);
```

The gateway verifies the token before upgrading and never echoes the
`bearer.` entry. A missing or invalid token fails the upgrade with HTTP 401
and a `google.rpc.Status` body, like any REST request; a forbidden resource
fails with 403 or 404. Cookies and query-string tokens are not accepted.

A connection is authorized once, at upgrade, and lives until the access
token expires. The gateway then closes it with `4401`; the client refreshes
the token (`POST /v1/sessions:refresh`) and reconnects. For sync that costs
one hello: the server sends only what the client's version vector lacks.
Access that is revoked while connected closes the connection with `4403`
(ADR-0008); `documents` also re-checks every batch of operations, so nothing
is accepted in between.

### Keepalive, rate limits and shutdown

- The gateway sends a WebSocket ping every 30 seconds and closes the
  connection with `1001` after 60 seconds without a pong. Clients need not
  ping.
- Frames count against the sync budget (`/sync`) or the agent budget
  (`/messages`). A client over budget is closed with `4429`; the close reason
  is the number of seconds to wait before reconnecting.
- On deploy, the gateway closes connections with `1012` (service restart);
  clients reconnect with backoff.
- Reconnect with exponential backoff and jitter, starting at 1 second and
  capped at 30 seconds, except after `4401` (refresh, then reconnect at once).

### Close codes

When the gRPC stream ends, the gateway closes the WebSocket with a code
derived from the gRPC status and a reason of at most 123 bytes taken from the
status message:

| Code | When | Client should |
|---|---|---|
| `1000` | Stream finished normally | Nothing |
| `1001` | Keepalive timeout, or the client navigated away | Reconnect |
| `1009` | Frame over 1 MiB | Fix the client |
| `1011` | `INTERNAL`, `UNKNOWN`, `DATA_LOSS` | Reconnect with backoff |
| `1012` | Gateway restarting, or `UNAVAILABLE` | Reconnect with backoff |
| `4400` | `INVALID_ARGUMENT`, `FAILED_PRECONDITION`, a frame that does not decode, a first sync frame that is not a hello | Fix the client; do not retry unchanged |
| `4401` | `UNAUTHENTICATED`, including token expiry | Refresh the token and reconnect |
| `4403` | `PERMISSION_DENIED`, including access revoked while connected | Stop; reload the resource over REST |
| `4404` | `NOT_FOUND` | Stop |
| `4429` | `RESOURCE_EXHAUSTED` | Wait the seconds in the reason, then reconnect |

## `DocumentService.Sync`

`GET /v1/notes/{noteId}/sync`. Live sync of one note (ADR-0005). The
gateway routes the connection to the `documents` instance that owns the note.

1. The client's first frame is a `SyncRequest` with `hello`: the note (which
   must equal the path's `noteId`), the client's registered replica
   (`POST /v1/replicas`) and its version vector. Anything else first closes
   with `4400`.
2. The server answers with the operations the client is missing, as one or
   more `ops` frames, then streams live operations from other replicas and
   `presence` updates.
3. The client sends its own edits as `ops` frames and its cursor as
   `presence` frames, at any time after the hello.
4. If the server refuses operations (for example, edits made offline after
   access was revoked), it sends `rejected` with those operations and a
   reason. The client drops them and keeps their text as a draft
   (ADR-0007).

`ops` is a batch in the wire format of
[`docs/spec/crdt-sequence.md`](../spec/crdt-sequence.md). Operations are
idempotent, so resending after a reconnect is safe. Presence is not stored
and is not echoed to the replica that sent it.

Client frames:

```json skein.v1.SyncRequest
{
  "hello": {
    "noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
    "replicaId": "4831727201934127107",
    "version": {
      "entries": [
        {"replicaId": "4831727201934127107", "counter": "42"},
        {"replicaId": "9120384756102938475", "counter": "17"}
      ]
    }
  }
}
```

```json skein.v1.SyncRequest
{"ops": "AQIDBAUGBwgJCgsMDQ4PEA=="}
```

```json skein.v1.SyncRequest
{
  "presence": {
    "userId": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
    "replicaId": "4831727201934127107",
    "anchor": "4831727201934127107:40",
    "head": "4831727201934127107:42"
  }
}
```

Server frames:

```json skein.v1.SyncResponse
{"ops": "AQIDBAUGBwgJCgsMDQ4PEA=="}
```

```json skein.v1.SyncResponse
{
  "presence": {
    "userId": "01936f3f-9d5b-7c3f-8a21-4e6b7c8d9ea1",
    "replicaId": "9120384756102938475",
    "anchor": "9120384756102938475:17",
    "head": "9120384756102938475:17"
  }
}
```

```json skein.v1.SyncResponse
{
  "rejected": {
    "ops": "AQIDBAUGBwgJCgsMDQ4PEA==",
    "reason": "access to the note was revoked"
  }
}
```

## `AgentService.SendMessage`

`GET /v1/chats/{chatId}/messages`. Talk to the agent in one chat
(ADR-0009). Each `SendMessageRequest` frame the client sends starts one
`SendMessage` call, that is one run; every event the run produces arrives as
a `SendMessageResponse` frame carrying its `runId`, so overlapping runs can be
told apart. `chatId` in a frame may be omitted; if present it must equal the
path's.

A run streams text deltas, tool calls and their results, citations, and ends
with exactly one `finished` event. Before a destructive or outward-facing
action it sends a `confirmation` and pauses until the client answers with
`POST /v1/runs/{runId}:confirm` ([agent.md](agent.md)). Cancelling is
`POST /v1/runs/{runId}:cancel`. Closing the socket does not cancel a run;
its events stay readable with `GET /v1/runs/{runId}`.

Client frame:

```json skein.v1.SendMessageRequest
{"text": "Summarise what changed in my Raft notes this week and link related notes."}
```

Server frames, in the order a run might produce them:

```json skein.v1.SendMessageResponse
{
  "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
  "toolCall": {
    "callId": "call_1",
    "tool": "search_notes",
    "argumentsJson": "{\"query\": \"raft\", \"changedSince\": \"2026-11-09\"}"
  }
}
```

```json skein.v1.SendMessageResponse
{
  "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
  "toolResult": {"callId": "call_1", "summary": "Found 3 notes"}
}
```

```json skein.v1.SendMessageResponse
{
  "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
  "textDelta": "Three notes changed. The biggest change is in "
}
```

```json skein.v1.SendMessageResponse
{
  "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
  "citation": {
    "location": {
      "noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
      "blockId": "4831727201934127107:17",
      "end": 54
    },
    "quote": "Snapshots bound the log and let slow followers catch up."
  }
}
```

```json skein.v1.SendMessageResponse
{
  "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
  "confirmation": {
    "confirmationId": "conf_1",
    "description": "Add a link from \"Raft log compaction\" to \"Raft snapshots\"",
    "tool": "apply_edit",
    "argumentsJson": "{\"noteId\": \"01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f\"}"
  }
}
```

```json skein.v1.SendMessageResponse
{
  "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
  "finished": {
    "outcome": "OUTCOME_COMPLETED",
    "steps": 4,
    "inputTokens": "5120",
    "outputTokens": "412"
  }
}
```

## `NotificationService.Subscribe`

`GET /v1/notifications/live`. Live notifications for the caller. The client
sends nothing: the gateway opens `Subscribe` on connect (its request is
empty) and relays each notification as a frame. Frames sent by the client are
ignored. Notifications missed while disconnected are not replayed here; after
reconnecting, read them with `GET /v1/notifications?unreadOnly=true`.

```json skein.v1.SubscribeResponse
{
  "notification": {
    "id": "01936f60-5e6f-7a80-9b92-0d1e2f304152",
    "kind": "NOTIFICATION_KIND_MENTION",
    "title": "Grace mentioned you in Raft log compaction",
    "body": "@Ada can you check the snapshot threshold?",
    "location": {
      "noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
      "blockId": "9120384756102938475:3"
    },
    "actorId": "01936f3f-9d5b-7c3f-8a21-4e6b7c8d9ea1",
    "createdAt": "2026-11-16T09:30:00Z"
  }
}
```
