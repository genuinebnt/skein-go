# Porting `services/agent`: port last, and only by rewriting the runtime

`agent` is a Python service (ADR-0015) implementing `AgentService` with
LangGraph and LangChain. It acts **as its user** (ADR-0009): it asks
identity for a delegated token for (user, run) and calls the same gRPC APIs
the frontend uses, so every permission check is the user's own. Destructive
and outward-facing tools pause the run until the user confirms. Retrieved
content is wrapped as untrusted data, answers cite the blocks they came
from, edits are made as CRDT operations in one `ApplyOps` batch (one undo
group), and every run is budgeted, traced and cancellable. The service
README (`README.md` here) is the detailed reference.

## Recommendation

**Keep the agent in Python until every Go service is in Rust. Then, if you
want it, port it as a from-scratch Rust runtime (no LangGraph), against the
existing contract, evals and e2e journeys.** This is optional, and it is
the owner's call; the reasons both ways:

For porting (eventually):

- The original design ([design/skein-rust-design.html](../../docs/design/skein-rust-design.html),
  "Agent") wanted the runtime written from scratch in Rust so the loop, the
  tools and the evals are understood end to end. The Python service took
  the opposite path (LangGraph) to ship; a Rust port is where that learning
  goal comes back.
- Everything around the runtime is already fixed and testable: the proto
  contract, the WebSocket framing, the confirmation rules, the eval harness
  with 12 tasks and a scripted model, and the e2e journeys
  `TestAgentSearchesAndCitesABlock` and
  `TestAgentDeletePausesForConfirmationAndDecliningKeepsTheNote`.
- The Python service carries its own port of the CRDT
  (`domain/crdt.py`) to make edits. A Rust agent uses `skein-crdt` directly,
  which removes one of the three CRDT implementations.

For keeping it in Python (now):

- LangGraph gives durable interrupts (paused runs survive restarts through
  its Postgres checkpointer), streaming and tool-call plumbing; in Rust you
  write the state machine and its persistence yourself.
- The model provider's Python SDK and LangChain's tool-calling adapters are
  mature; Rust agent frameworks are young (check the current state of
  [Rig](https://docs.rig.rs/) before choosing one, or call the provider's
  HTTP API with `reqwest`).
- The agent is the least performance-sensitive service: time is spent
  waiting on the model.
- Porting it before the Go services competes with the main goal.

## Contract (what a Rust agent must match)

### RPCs (`proto/skein/v1/agent.proto`, `docs/api/agent.md`)

| RPC | Route | Python |
|---|---|---|
| `CreateChat` | `POST /v1/workspaces/{workspace_id}/chats` | `app/chats.py` |
| `ListChats` | `GET /v1/workspaces/{workspace_id}/chats` | `app/chats.py` |
| `SendMessage` | WebSocket `GET /v1/chats/{chatId}/messages` (each frame one run, server-streamed events) | `AgentRuns.send_message` (`app/runs.py`) |
| `Confirm` | `POST /v1/runs/{run_id}:confirm` | resumes a paused run |
| `CancelRun` | `POST /v1/runs/{run_id}:cancel` | the only way to cancel; leaving the stream cancels nothing |
| `GetRun` | `GET /v1/runs/{run_id}` | the stored trace |

Run events (text deltas, tool calls and results, `ConfirmationRequest`,
`Citation`, `RunFinished` with outcome and usage) are framed as in
`docs/api/websocket.md` ("AgentService.SendMessage").

### Calls

Identity `IssueDelegatedToken` (once per run, with the user's token),
`Authorize` (before asking for a confirmation), `RegisterReplica` (once,
for edits); then, with the delegated token, the tools: search (`Search`,
`SuggestTitles`, `BlastRadius`, `ShortestPath`, `ReadingPlan`), documents
(`GetNote`, `ListNotes`, `CreateNote`, `RenameNote`, `DeleteNote`, `Sync`
to fetch the op log, `ApplyOps`), ml (`SemanticSearch`, `Discover`,
`ListClusters`), identity (`SetNoteRole`, `CreateShareLink`) and
notifications (`ListInbox`). The catalogue is `app/tools/catalogue.py`; tool
argument schemas are derived from the proto request descriptors and the
descriptions from proto comments (`gen/python/.../descriptor_set.binpb`).

### Events

`deploy/compose/pubsub-init.sh` has an `agent.events` topic, and
notifications consumes it (`Producer.AgentEvent`: run finished, or a
confirmation is waiting), but the agent does not publish to it yet. A port
(or the Python service) should add an outbox for it.

### Storage

Schema `agent` (`migrations/0001_init.sql`): `chats`, `chat_turns`, `runs`
(one unfinished run per chat), `run_entries` (the trace). LangGraph's
checkpointer tables hold paused graph state. Redis: per-user read cache
(30 s), the per-user run rate limit (GCRA, `domain/ratelimit.py`).

### Environment

`SKEIN_AGENT_ADDR`, `SKEIN_AGENT_ADMIN_ADDR`, `SKEIN_AGENT_STORAGE`,
`SKEIN_AGENT_POSTGRES_DSN`, `SKEIN_AGENT_REDIS_URL`,
`SKEIN_AGENT_MODEL_BACKEND` (`anthropic` or `scripted`),
`SKEIN_AGENT_MODEL`, `ANTHROPIC_API_KEY`, the budgets
(`SKEIN_AGENT_MAX_STEPS`, `_MAX_TOKENS`, `_MAX_OUTPUT_TOKENS`,
`_MAX_COST_USD`, `_MAX_WALL_SECONDS`, `_PRICE_INPUT_PER_MTOK`,
`_PRICE_OUTPUT_PER_MTOK`), `SKEIN_AGENT_RUNS_PER_MINUTE`, `_RUN_BURST`,
`SKEIN_AGENT_CACHE_TTL_SECONDS`, `SKEIN_AGENT_CONFIRMATION_TIMEOUT_SECONDS`,
`SKEIN_AGENT_SWEEP_SECONDS` (`config.py`).

## Learn first

| Topic | Read | What to take from it |
|---|---|---|
| agent design | [Building effective agents (Anthropic)](https://www.anthropic.com/engineering/building-effective-agents) | the augmented-LLM loop, when a workflow beats an agent, why tools need clear contracts |
| tool use | [Claude tool use overview](https://docs.claude.com/en/docs/agents-and-tools/tool-use/overview) | tool definitions with JSON Schema, `tool_use` and `tool_result` blocks, streaming |
| LangGraph | [LangGraph overview](https://docs.langchain.com/oss/python/langgraph/overview) | what the Python service relies on: nodes, interrupts, checkpointers; the parts a Rust runtime must replace |
| prompt injection | [Prompt injection (Simon Willison)](https://simonwillison.net/2022/Sep/12/prompt-injection/) | why retrieved text is wrapped as data with a per-run nonce, and why the confirmation gate is the real defence |
| Rust agents | [Rig](https://docs.rig.rs/) | a Rust toolkit for LLM agents, useful as a reference even if you write your own loop |
| descriptors to schemas | [prost-reflect](https://docs.rs/prost-reflect/latest/prost_reflect/) | walking request message descriptors to build JSON Schemas and parsing arguments back as `DynamicMessage` |
| cancellation | [CancellationToken](https://docs.rs/tokio-util/latest/tokio_util/sync/struct.CancellationToken.html), [Tokio graceful shutdown](https://tokio.rs/tokio/topics/shutdown) | `CancelRun` cancels the run's task, which cancels the in-flight gRPC call |
| HTTP client | [reqwest](https://docs.rs/reqwest/latest/reqwest/) | streaming server-sent events from the model API |

## Port order (if and when)

The Python layout already separates the layers, so the Rust order follows
it. Put `domain/`, `app/` and `fakes/` in `agent-core`; adapters and the
binary in `agent`.

### 1. Domain (pure)

| Python file | Job | Rust | Proven by |
|---|---|---|---|
| `domain/ids.py`, `errors.py` | IDs and typed errors | newtypes, a `thiserror` enum | `tests/test_domain.py` |
| `domain/budget.py` | steps, tokens, cost and wall time; reserve before each step; waiting for a confirmation does not count | a struct with `checked_*` arithmetic | `tests/test_domain.py` |
| `domain/policy.py` | effects (read, write, destructive, outward) and which need confirmation; no setting skips it | an enum with a `needs_confirmation` method | `tests/test_domain.py`, `tests/test_tools.py` |
| `domain/untrusted.py` | wrap retrieved content in `<untrusted_data nonce="...">`, defang tag-like text, number sources `S1, S2...`, parse `[S2]` citations | string building; keep the exact format, the prompt depends on it | `tests/test_injection.py` |
| `domain/events.py` | the run's event types | an enum mirroring the proto events | `tests/test_runs.py` |
| `domain/ratelimit.py` | GCRA for runs per user | reuse `skein-ratelimit` | `tests/test_domain.py` |
| `domain/edits.py` | resolve find-and-replace edits against the replayed text into position edits | pure functions over `char` indexes | `tests/test_edit.py` |
| `domain/crdt.py` | the Python port of the sequence CRDT | delete it: use `skein-crdt` | `tests/test_crdt.py` becomes the crdt crate's own fixtures |

### 2. Application

| Python file | Job | Rust | Proven by |
|---|---|---|---|
| `app/ports.py` | `ServiceClient.invoke(rpc, request, token, timeout)`, op-log fetch, model backend, run store, chat store, cache, rate limiter, clock | traits behind `Arc<dyn _>` | the fakes implement them |
| `app/tools/schema.py` | JSON Schema from proto request descriptors, hiding agent-filled fields such as `workspace_id` | `prost-reflect` over `skein-proto`'s descriptor set | `tests/test_tools.py` |
| `app/tools/catalogue.py`, `base.py`, `render.py` | one entry per RPC: name, service, request type, effect, renderer | a static table | `tests/test_tools.py` |
| `app/tools/executor.py` | run calls in order; refuse any confirmation-requiring call without a recorded approval; map permission errors to tool results that tell the model to explain, not work around | | `tests/test_tools.py` |
| `app/tools/edit.py` | `apply_edit`: read the note, register a replica once, fetch the op log through `Sync`, replay into the CRDT, check the text, resolve edits, send one `ApplyOps` batch | `skein-crdt`, `skein-blocks` | `tests/test_edit.py` |
| `app/prompts.py` | the system prompt | a `const` string | the evals |
| `app/graph.py` | model, gate, tools as a LangGraph graph with `interrupt` | a hand-written state machine: `enum Step { Model, Gate, Tools, Done }` with the state serialisable, so a pause can be stored and resumed after a restart | `tests/test_runs.py`, `tests/test_restart.py` |
| `app/runs.py` | `send_message`, the driver task (segments, pauses, resume, cancel, timeouts), outcomes, `expire_stale` | Tokio tasks with a `CancellationToken` per run, a `broadcast` or `mpsc` channel for events | `tests/test_runs.py`, `tests/test_restart.py` |
| `app/chats.py` | chats and their turns | | `tests/test_stores.py` |

### 3. Fakes, adapters, evals

- `fakes/workspace.py` (an in-memory workspace for all six services),
  `fakes/model.py` (a scripted chat model), `fakes/harness.py`: port the
  scripted model first; it makes every loop test deterministic.
- `adapters/grpc_server.py` (tonic service), `grpc_clients.py` (one
  generic client: a tonic `Grpc` with a dynamic codec, or the typed clients
  from `skein-proto`), `anthropic_model.py` (the provider's HTTP API with
  streaming), `postgres.py` and `memory.py` (stores; the checkpointer tables
  become your own `paused_runs` state), `redis_store.py`, `logging.py`,
  `telemetry.py`, `admin.py`, `wiring.py`.
- `evals/` (fixture workspace, 12 tasks, runner): port the runner and keep
  the task definitions as data shared with the Python version, so both
  runtimes are scored on the same tasks with the scripted model and, with a
  key, the live model.

## Pitfalls

- **Re-execution on resume.** LangGraph re-runs an interrupted node from
  the start, so the Python gate has no side effects and each approval names
  the call it approves. A hand-written state machine can avoid the
  re-execution, but keep approvals bound to the call ID anyway.
- **Delegated token everywhere.** Every downstream call carries the
  delegated token, never the user's own token and never a service
  credential; the services check it like any user token.
- **Cancellation is explicit.** A client leaving the stream must not cancel
  the run; only `CancelRun`, a declined confirmation, a timeout or shutdown
  do.
- **No content in logs.** Log by error type and IDs, never prompts, tool
  results or note text.
- **Budgets bound downstream calls.** The wall-time deadline is passed into
  every gRPC call as its deadline.

## Done when (for a Rust agent)

- the Python tests' scenarios pass against the Rust runtime with the
  scripted model;
- the eval harness scores the Rust runtime at least as well as the Python
  one on the same tasks;
- `make e2e` passes `TestAgentSearchesAndCitesABlock` and
  `TestAgentDeletePausesForConfirmationAndDecliningKeepsTheNote` with the
  Rust agent.
