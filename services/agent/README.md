# agent

Skein's AI agent: a Python gRPC service implementing `AgentService`
(`proto/skein/v1/agent.proto`) with LangGraph and LangChain. The agent acts
**as its user** through the same gRPC APIs the frontend uses, with the user's
permissions and nothing more ([ADR-0009](../../docs/adr/0009-agent-acts-as-the-user.md),
[ADR-0015](../../docs/adr/0015-ml-and-agent-in-python.md)).

## Quick start

```
cd services/agent
python3 -m venv .venv && .venv/bin/pip install -e ".[dev,postgres]"   # or: uv sync --extra dev --extra postgres
.venv/bin/python scripts/gen_proto.py      # stubs into gen/ (git-ignored)
.venv/bin/python -m pytest                 # no network, no Docker, no API key
.venv/bin/python -m skein_agent.evals      # eval harness with the scripted model
ANTHROPIC_API_KEY=... .venv/bin/python -m skein_agent.evals --model anthropic --json eval-report.json
ANTHROPIC_API_KEY=... .venv/bin/python -m skein_agent          # serve on :50057
docker build -f services/agent/Dockerfile -t skein-agent .     # from the repo root
```

`pytest -m integration` runs the Postgres checkpointer test (needs Docker and
`testcontainers`); `pytest -m live_model` runs the evals against the real
model (needs a key). Both are skipped by default.

**Generated stubs.** `scripts/gen_proto.py` writes `skein.v1` stubs and a
descriptor set with source info into `gen/`. A parallel change adds generated
Python under the repository's `gen/python`; when it lands, install that and
delete the script and the path fallback in `src/skein_agent/pb.py`. Imports
elsewhere do not change.

## Layout

```
src/skein_agent/
  domain/      pure: budgets, run events, untrusted-content wrapping and citations,
               GCRA, tool effects, edit planning, the sequence CRDT (Python port)
  app/         use cases (runs.py, chats.py), the LangGraph graph (graph.py),
               the system prompt, ports.py, and tools/ (schema, catalogue, executor)
  adapters/    gRPC server and clients, Anthropic, Redis, Postgres checkpointer,
               in-memory stores, JSON logging, OpenTelemetry, wiring for main
  fakes/       in-memory workspace for all six services, scripted chat model,
               cache, rate limiter, clock, and a harness that wires them
  evals/       fixture workspace, 12 tasks, runner, CLI
tests/         pytest; every port is faked
```

## How it works

**Graph.** `model → gate → tools → model ...` (see `app/graph.py`). The model
node streams text deltas and reserves a step from the budget first. The gate
pauses with a LangGraph `interrupt` for every destructive or outward-facing
call, after checking with `identity.Authorize` that the user may make it at
all. The tools node runs calls in order and refuses any
confirmation-requiring call that has no recorded approval, whatever path led
there.

**Tools.** One `RpcTool` entry per RPC in `app/tools/catalogue.py`. Argument
schemas are derived from the proto request descriptors (`app/tools/schema.py`),
descriptions from proto comments, and arguments are parsed back with
`json_format.ParseDict`. Agent-filled fields (`workspace_id`) are hidden from
the model. `apply_edit` is the one composite tool (see below). All tools go
through a single `ServiceClient.invoke(rpc, request, token, timeout)` port,
so a new RPC needs no client code.

| Effect | Tools | Confirmation | Cached |
|---|---|---|---|
| read | search, suggest_titles, get_note, list_notes, blast_radius, shortest_path, reading_plan, semantic_search, discover, list_clusters, list_inbox | no | per user, 30 s |
| write | create_note, rename_note, apply_edit | no | clears the user's cache |
| destructive | delete_note | **always** | no |
| outward | set_note_role, create_share_link | **always** | no |

Confirmation is derived from the effect (`domain/policy.py`); there is no
setting that skips it.

**Delegated token.** `SendMessage` asks identity for a token for
(user, run) using the user's own bearer token, once. Every tool call carries
the delegated token; services check it like any user token. Permission
errors come back to the model as tool results telling it to explain the
refusal and not work around it.

**Untrusted content.** Everything retrieved is wrapped in
`<untrusted_data nonce="...">` blocks with a random per-run nonce; tag-like
text inside is defanged so content cannot close its block. The system prompt
says block contents are data. The gate means a fooled model can at most
*propose* an action the user then sees described from real data and can decline.
Tests: `tests/test_injection.py`; eval task `injection-meeting`.

**Citations.** Retrieved locations are numbered `S1, S2...`; the model cites
`[S2]`, and the run emits a `Citation` (BlockRef and quote) for each marker
that names a retrieved source. Unknown markers are dropped and recorded.

**Edits as suggestions.** `apply_edit` reads the note, registers a replica for
the run with identity (once), fetches the op log through `Sync`, replays it
into the Python CRDT port (`domain/crdt.py`, passes the shared conformance
fixtures and golden bytes), checks the replayed text matches, resolves
find/replace edits, and sends all generated ops in **one** `ApplyOps` batch,
which is one change group for `Undo`.

**Budgets.** Steps (model calls), tokens, cost (list prices for current Claude
models, overridable), and wall time. Steps, tokens and cost are checked before
each step; wall time is a deadline that also bounds every downstream call.
Waiting for a confirmation does not count. Exhaustion ends the run with
`OUTCOME_BUDGET_EXCEEDED`.

**Cancellation.** Each graph segment runs in its own task; `CancelRun`
cancels it, which cancels the in-flight gRPC call. A client that disconnects
from `SendMessage` cancels its run.

**Traces.** Every event plus model calls (tokens, cost, latency), tool calls
(latency, error code, cached, the result the model saw, with share-link
tokens redacted) and messages go to a `RunStore` (in memory for now).
`GetRun` returns the user-visible events.

**Observability.** JSON logs with `trace_id`, `span_id`, `request_id`,
`user_id`, `run_id`; only allow-listed fields are logged, never note text or
tokens. Spans `agent.run`, `agent.model`, `agent.tool`; metrics
`agent.runs`, `agent.run.steps`, `agent.tokens`, `agent.cost`,
`agent.run.duration`, `agent.tool.calls`, `agent.tool.duration`,
`agent.confirmations`, `agent.rate_limited`. Configured by `OTEL_*`
(`adapters/telemetry.py`); off unless an OTLP endpoint is set.

**Redis.** Tool-result cache and a GCRA per-user run limit (Lua, same
arithmetic as `domain/ratelimit.py`, tested against it with fakeredis).
Without `SKEIN_AGENT_REDIS_URL` both are per process.

Configuration: every variable is listed in `src/skein_agent/config.py`.

## Decisions to confirm, and contract gaps

- **Suggestions have no flag in the contracts.** `apply_edit` relies on
  documents treating change groups from agent-run replicas as suggestions;
  rejecting one is `Undo(note_id, replica_id)`. An explicit field (or a
  `SuggestOps` RPC) in `documents.proto` would make this visible.
- **`GetRunResponse` carries only stream events.** The richer trace (tokens,
  cost and latency per call) is stored but has no proto field; a `RunTrace`
  message would expose it to the UI and evals.
- **No email RPC exists** in `notifications.proto`, so there is no
  `send_email` tool yet; when one is added it is `Effect.OUTWARD` and gated.
- **Declining a confirmation ends the run** as `OUTCOME_CANCELLED`
  (agent.proto: "the paused run continues or stops"), and nothing from that
  batch of tool calls runs.
- **Authentication** trusts the gateway's `x-skein-user-id` header plus the
  forwarded bearer token (only used to obtain the delegated token, so a forged
  header without the user's token gets nothing). Verifying the token's
  signature locally is a follow-up once identity publishes its key format.
- **Share-link tokens** go to the model so it can hand them to the user who
  asked (after confirming); they are redacted from traces and never logged.
- **Each run is its own LangGraph thread**; earlier turns come from the chat
  transcript, so no run inherits half-finished state from another.
- **Chats, traces and runs are in memory** and runs are driven in-process;
  the Postgres checkpointer keeps paused graph state, but resuming a paused
  run on another replica after a restart is a follow-up (with an `agent`
  schema for chats and traces).
