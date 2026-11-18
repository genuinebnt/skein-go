# Agent

`AgentService` ([`agent.proto`](../../proto/skein/v1/agent.proto)): chats
with the agent, its runs and confirmations ([ADR-0009](../adr/0009-agent-acts-as-the-user.md)).
Served by the Python `agent` service. The agent acts as the caller, with the
caller's permissions and nothing more. Sending a message and receiving the
run's output is the `/v1/chats/{chatId}/messages` WebSocket
([websocket.md](websocket.md#agentservicesendmessage)). Conventions are in
[README.md](README.md).

| Method | Path | RPC |
|---|---|---|
| `POST` | `/v1/workspaces/{workspaceId}/chats` | CreateChat |
| `GET` | `/v1/workspaces/{workspaceId}/chats` | ListChats |
| `POST` | `/v1/runs/{runId}:confirm` | Confirm |
| `POST` | `/v1/runs/{runId}:cancel` | CancelRun |
| `GET` | `/v1/runs/{runId}` | GetRun |

### CreateChat

`POST /v1/workspaces/{workspaceId}/chats`. `noteId` optionally anchors the
chat to a note, which the agent then reads first. Send an `Idempotency-Key`.

```json skein.v1.CreateChatRequest
{"noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f"}
```

```json skein.v1.CreateChatResponse
{"chatId": "01936f50-6a7b-7c8d-9e0f-1a2b3c4d5e6f"}
```

### ListChats

`GET /v1/workspaces/{workspaceId}/chats`. The caller's chats, most recently
updated first. Paginated.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/chats?page.pageSize=20
```

```json skein.v1.ListChatsResponse
{
  "chats": [
    {
      "chatId": "01936f50-6a7b-7c8d-9e0f-1a2b3c4d5e6f",
      "title": "This week's Raft changes",
      "updatedAt": "2026-11-16T10:05:00Z"
    }
  ],
  "page": {}
}
```

### Confirm

`POST /v1/runs/{runId}:confirm`. Answers a `confirmation` event. A paused
run continues if `approved` is true and stops without acting otherwise.
Answering the same confirmation twice is `FAILED_PRECONDITION`.

```json skein.v1.ConfirmRequest
{"confirmationId": "conf_1", "approved": true}
```

```json skein.v1.ConfirmResponse
{}
```

### CancelRun

`POST /v1/runs/{runId}:cancel`. Stops the run at its next step; it then
sends `finished` with `OUTCOME_CANCELLED`. Cancelling a finished run does
nothing.

```json skein.v1.CancelRunRequest
{}
```

```json skein.v1.CancelRunResponse
{}
```

### GetRun

`GET /v1/runs/{runId}`. The run's stored trace: every event it streamed, in
order, in the same shape as the WebSocket frames. Used to show a run after
reconnecting or later.

```
GET /v1/runs/01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f
```

```json skein.v1.GetRunResponse
{
  "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
  "events": [
    {
      "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
      "toolCall": {
        "callId": "call_1",
        "tool": "search",
        "argumentsJson": "{\"query\": \"raft\"}"
      }
    },
    {
      "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
      "toolResult": {"callId": "call_1", "summary": "Found 3 notes"}
    },
    {
      "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
      "textDelta": "Three notes changed this week."
    },
    {
      "runId": "01936f51-7a8b-7c9d-8e0f-1a2b3c4d5e6f",
      "finished": {
        "outcome": "OUTCOME_COMPLETED",
        "steps": 2,
        "inputTokens": "3100",
        "outputTokens": "96"
      }
    }
  ]
}
```
