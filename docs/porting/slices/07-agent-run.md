# Slice 7: one agent run

A user opens a chat and asks the agent to search; the agent calls search
as the user, answers, and cites the block it found. Then the user asks it to
delete a note; the run pauses with a confirmation request, the user
declines, and the note is still there. The agent itself stays in Python
(see [services/agent/PORTING.md](../../../services/agent/PORTING.md)); this
slice ports the Go side of an agent run, which touches four services, and
shows where a Rust agent would plug in.

## The path

```
browser WebSocket GET /v1/chats/{chatId}/messages  (frame: {text: "/search raft"})
gateway
  wsapi.Handlers.serveAgent: verify token, agent budget per frame
  startRun: fresh request ID, AgentService.SendMessage (server stream), detached from the socket
agent (Python)
  AgentRuns.send_message: chat check, run rate limit (GCRA)
    identity IssueDelegatedToken(user token, run_id)   -> token with agent_run_id
  graph: model -> gate -> tools -> model ...
    tool "search": SearchService.Search with the delegated token
search
  grpcx auth interceptor: platform/auth verifies the delegated token;
  claims.Subject{user, agent_run_id}; Access.Readable is the user's own
agent
  wraps results as untrusted data, numbers sources S1..; model cites [S1]
  events: text deltas, tool call, tool result, Citation, RunFinished
gateway: relays each event as a frame; the run keeps going if the socket closes

delete:
  model asks for delete_note -> gate: identity Authorize(DELETE) as the user
  -> interrupt -> ConfirmationRequest frame -> run paused (durable)
  browser POST /v1/runs/{runId}:confirm {confirmationId, approved: false}
  -> run ends CANCELLED; documents DeleteNote is never called
```

## Contracts and storage

- Protos: `agent.proto` (all six RPCs, run events, `ConfirmationRequest`,
  `Citation`, outcomes), `identity.proto` (`IssueDelegatedToken`,
  `Authorize`, `RegisterReplica`), `search.proto` (`Search`),
  `documents.proto` (`GetNote`, `DeleteNote`; `Sync` and `ApplyOps` for
  edits), `events.proto` (`NotificationRequested` on `agent.events`).
- Token format: `platform/auth` (`agent_run_id` claim; a delegated token
  carries the user's identity, ADR-0009).
- Migration: `services/agent/migrations/0001_init.sql` (Python service).
- WebSocket framing: `docs/api/websocket.md`, "AgentService.SendMessage".

## Files to port, in order (the Go side)

Prerequisites: slices 1, 2, 3 and 5.

| # | Go file | What this slice needs |
|---|---|---|
| 1 | `platform/auth/token.go` | `Subject.AgentRunID`, `IsAgent`, issuing and verifying the `agent_run_id` claim |
| 2 | `platform/logging/context.go` | `WithAgentRunID`, so every log line of the run carries it |
| 3 | `services/identity/internal/app/authorize.go` | `IssueDelegatedToken` (only for yourself, short TTL capped by `IDENTITY_DELEGATED_TOKEN_MAX_TTL`, carries the run) |
| 4 | `services/identity/internal/domain/policy.go` | agent subjects decided exactly as their user; `AGENT_RUN` on the workspace |
| 5 | `services/identity/internal/adapters/grpc/server.go` | `IssueDelegatedToken` |
| 6 | `services/search/internal/app/access.go`, `search.go` | nothing new if slice 3 is done: the delegated token is a user token |
| 7 | `services/documents/internal/domain/note.go` | `Subject.IsAgent` |
| 8 | `services/documents/internal/domain/batch.go`, `history.go` | the agent suggestion batch kind: each `ApplyOps` call from a run is its own change group |
| 9 | `services/documents/internal/app/service.go` | `ApplyOps` for agent subjects (one group per call), `DeleteNote` |
| 10 | `services/notifications/internal/app/events.go` | `AgentEvent` (only to the run's own user) |
| 11 | `services/notifications/internal/domain/rules.go` | `AgentCandidates` |
| 12 | `services/notifications/internal/adapters/pubsub/consumers.go` | the `notifications.agent-events` subscription |
| 13 | `services/gateway/internal/adapters/wsapi/handlers.go` | `serveAgent`, `startRun`, `StopRuns`, `finished` |
| 14 | `services/gateway/internal/adapters/httpapi/server.go` | `Drain` gives detached runs what is left of the deadline |
| 15 | `services/gateway/internal/domain/policy.go`, `limits.go` | the agent budget |

## Tests that prove it

Identity: `TestDelegatedTokensCarryTheRunAreShortAndOnlyForYourself`,
`TestDelegatedTokenActsForTheUserWithTheRunID`; platform:
`TestIssueAndVerifyRoundTripsUserAndAgentClaims`; documents:
`TestAgentSuggestionIsUndoneAsOneGroup`; notifications:
`TestAgentRunsNotifyTheirUserAndConfirmationsHaveTheirOwnSource`; gateway:
the agent cases in `httpapi/websocket_test.go` (runs relayed as they come,
detached on close).

Python, unchanged, as the contract check for the Go side:
`services/agent/tests/test_grpc.py`, `test_runs.py`, `test_injection.py`.

End to end (`e2e/agent_test.go`, run with the agent's offline model, which
understands `/search <query>` and `/delete <note id>`):
`TestAgentSearchesAndCitesABlock`,
`TestAgentDeletePausesForConfirmationAndDecliningKeepsTheNote`.

## Stubbed or missing

- **`agent.events` has no publisher.** Notifications consumes it, but the
  Python agent does not publish run-finished or confirmation events yet, so
  `AGENT_RUN_FINISHED` notifications only come from the seed. Add an outbox
  to the agent (in Python, or in a Rust agent).
- The agent runtime: Python. To port it, follow
  [services/agent/PORTING.md](../../../services/agent/PORTING.md); this
  slice's e2e journeys and the eval harness are its acceptance tests, and
  `skein-crdt` replaces `domain/crdt.py` for edits.
