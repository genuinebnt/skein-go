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
.venv/bin/python -m pytest                 # no network, no Docker, no API key
.venv/bin/python -m skein_agent.evals      # eval harness with the scripted model
ANTHROPIC_API_KEY=... .venv/bin/python -m skein_agent.evals --model anthropic --json eval-report.json
ANTHROPIC_API_KEY=... SKEIN_AGENT_POSTGRES_DSN=postgresql://... .venv/bin/python -m skein_agent   # gRPC :9090, /healthz /readyz :8080
SKEIN_AGENT_STORAGE=memory SKEIN_AGENT_MODEL_BACKEND=scripted .venv/bin/python -m skein_agent      # no database
docker build -f services/agent/Dockerfile -t skein-agent .     # from the repo root
```

Compose: [`deploy/compose/services/agent.yml`](../../deploy/compose/services/agent.yml)
publishes gRPC on `127.0.0.1:9107` and the admin endpoints on
`127.0.0.1:8107` (container ports 9090 and 8080), with Postgres (role and schema `agent`) and
Redis database 2, and the scripted model unless `SKEIN_AGENT_MODEL_BACKEND`
and `ANTHROPIC_API_KEY` are set.

`pytest -m integration` runs the store contract and restart tests on
Postgres: against `SKEIN_AGENT_TEST_DATABASE_URL` if set (a disposable
database; the tests truncate the `agent` tables), otherwise on a
Testcontainers Postgres (needs Docker and `testcontainers`).
`pytest -m live_model` runs the evals against the real model (needs a key).
Both are skipped by default.

**Generated stubs.** Messages, stubs and a descriptor set with source info
(for tool descriptions) come from the shared package in
[`gen/python`](../../gen/python) (`skein_proto.v1`), a path dependency in
`pyproject.toml` and `uv.lock`. Everything imports them through
`src/skein_agent/pb.py`. After `make proto` changes them, reinstall (pip)
or `uv sync`.

## Layout

```
src/skein_agent/
  domain/      pure: budgets, run events, untrusted-content wrapping and citations,
               GCRA, tool effects, edit planning, the sequence CRDT (Python port)
  app/         use cases (runs.py, chats.py), the LangGraph graph (graph.py),
               the system prompt, ports.py, and tools/ (schema, catalogue, executor)
  adapters/    gRPC server and clients, Anthropic, Redis, Postgres stores and
               checkpointer, in-memory stores, JSON codec, JSON logging,
               OpenTelemetry, wiring for main
  fakes/       in-memory workspace for all six services, scripted chat model,
               cache, rate limiter, clock, and a harness that wires them
  evals/       fixture workspace, 12 tasks, runner, CLI
tests/         pytest; every port is faked (Postgres only under -m integration)
migrations/    SQL for the agent schema, applied at startup
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
cancels it, which cancels the in-flight gRPC call. Only `CancelRun` cancels:
a client that disconnects from `SendMessage` leaves the run going to
completion or to a confirmation pause, with every event kept in the
RunStore, so `GetRun` returns them and `Confirm` answers the pause
(`docs/api/websocket.md`).

**Storage.** Chats, runs (status, traces, paused state, usage) and LangGraph
checkpoints are in Postgres, schema `agent` (`migrations/`, applied at
startup under an advisory lock; the checkpointer's tables are created there
too). `SKEIN_AGENT_STORAGE=memory` keeps all three in memory for local runs;
the tests use the in-memory stores and check both against one contract
(`tests/test_stores.py`).

**Durable pauses.** A run paused on a confirmation stores what it needs to
resume (workspace, untrusted-data nonce, citation sources, CRDT replica,
budget and wall time used, the pending confirmation), and its graph state is
in the checkpointer. Shutdown leaves it paused (a client disconnect never
cancels anything).
Confirm on any process resumes it there, with a new delegated token obtained
with the confirming user's own token (no token is stored). Every way out of
a pause (Confirm, CancelRun, the timeout) first claims it with a
compare-and-set in the RunStore, so exactly one process acts, whether the
pause is live here, was left by a restart, or is live on another replica.
A sweeper on every replica (`SKEIN_AGENT_SWEEP_SECONDS`) ends pauses past
the confirmation timeout as CANCELLED, and runs left running by a stopped
process (running longer than the wall-time budget plus a minute) as FAILED,
which also frees their chat. Tests: `tests/test_restart.py`.

**Traces and usage.** Every event plus model calls (tokens, cost, latency),
tool calls (latency, error code, cached, the result the model saw, with
share-link tokens redacted) and messages go to the `RunStore`; a finished
run also records its totals (steps, tokens, cost). `GetRun` returns the
user-visible events, whose `RunFinished` carries steps and tokens. Cost has
no field in `GetRunResponse`, so GetRun also sends header metadata
`x-skein-run-usage` (gRPC callers only: the gateway forwards no backend
header to REST clients) with JSON: `status`, `steps`, `input_tokens`, `output_tokens`,
`cost_usd` and `model_calls` (step, tokens, cost, latency per call). For a
run in progress the totals are summed from the model calls so far.

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
- **`GetRunResponse` has no usage field.** Tokens reach clients through
  `RunFinished`; cost and per-call figures only through the
  `x-skein-run-usage` header. Proposed contract change:
  `message RunUsage { uint32 steps = 1; uint64 input_tokens = 2; uint64
  output_tokens = 3; double cost_usd = 4; repeated ModelCall model_calls = 5; }`
  with `message ModelCall { uint32 step = 1; uint64 input_tokens = 2; uint64
  output_tokens = 3; double cost_usd = 4; double latency_ms = 5; }`, and
  `RunUsage usage = 3;` in `GetRunResponse`; the header then goes.
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
- **CancelRun for a run *working* on another replica** cannot reach it (the
  contract has no fan-out); it stops at its wall-time deadline. A paused run
  is cancelled from any replica.
- **A client disconnect never cancels a run** (it used to), matching
  `docs/api/websocket.md`: the run finishes or pauses, and the client reads
  it with GetRun. A run nobody follows still ends at its budgets or its
  confirmation timeout.
- **When two replicas race for one pause and the original driver loses**,
  its SendMessage stream ends without `RunFinished`; the client reads the
  outcome with GetRun.
