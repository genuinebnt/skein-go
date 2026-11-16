"""Agent runs: starting, streaming, confirming, cancelling, tracing.

A run answers one user message. ``AgentRuns.send_message`` checks the chat
and the user's rate limit, gets a delegated token for (user, run) from
identity (ADR-0009), and starts a driver task. The caller streams the run's
events; the driver keeps going while it waits for a confirmation, because
Confirm arrives on another RPC.

Driver loop::

    segment: run the graph until it ends or interrupts
      interrupt → emit ConfirmationRequest, wait for Confirm (or cancel, or
                  timeout), resume the graph with the answer
      end       → emit citations, store the transcript
    always: emit RunFinished with the outcome and usage

Outcomes:

- COMPLETED: the graph reached END normally.
- CANCELLED: CancelRun, the user declined a confirmation, the confirmation
  timed out, or the client went away.
- BUDGET_EXCEEDED: no step left (steps, tokens, cost), or the wall-time
  deadline fired. Waiting for the user does not count against wall time.
- FAILED: anything else (logged by error type, never with content).

Cancellation works mid-tool-call: each graph segment runs in its own task,
and cancelling that task cancels the awaiting downstream gRPC call.

Each run has its own LangGraph thread (thread ID = run ID). Earlier turns of
the chat are passed in as messages from the transcript, so a run never
inherits half-finished state (dangling tool calls, pending interrupts) from
an earlier one.
"""

from __future__ import annotations

import asyncio
import logging
import os
import time
from collections.abc import AsyncIterator, Sequence
from dataclasses import dataclass, field
from typing import Any

from google.protobuf.timestamp_pb2 import Timestamp
from langchain_core.messages import AIMessage, BaseMessage, HumanMessage
from langgraph.checkpoint.base import BaseCheckpointSaver
from langgraph.types import Command

from .. import pb
from ..domain.budget import Budget, BudgetExceededError, BudgetKind, BudgetTracker, Pricing
from ..domain.errors import AgentError, AgentErrorKind, ErrorCode, ServiceError
from ..domain.events import (
    CitationAdded,
    ConfirmationRequested,
    MessageRecord,
    ModelCallRecord,
    NoteRecord,
    Outcome,
    RunEvent,
    RunFinished,
    TraceEntry,
    TraceRecord,
)
from ..domain.ids import uuid7
from ..domain.untrusted import SourceRegistry, extract_citations
from ..observability import Instruments, run_id_var, tracer
from .graph import DECLINED, RunContext, bind_model, build_graph, message_text
from .ports import ChatStore, ChatTurn, Clock, ModelProvider, RateLimiter, RunStore, Services
from .prompts import SYSTEM_PROMPT
from .tools.base import ToolRunContext
from .tools.executor import ToolExecutor

log = logging.getLogger(__name__)


@dataclass(frozen=True, slots=True)
class Principal:
    """The authenticated caller. ``access_token`` is the user's own token,
    used once per run to obtain the delegated token, and never logged."""

    user_id: str
    access_token: str


@dataclass(frozen=True)
class RunSettings:
    """Limits and timings for runs."""

    budget: Budget = field(default_factory=Budget)
    pricing: Pricing = field(default_factory=lambda: Pricing(2.0, 10.0))
    confirmation_timeout_s: float = 600.0
    history_turns: int = 10
    call_timeout_s: float = 15.0
    max_result_chars: int = 24_000


class _Cancelled(Exception):
    """Internal: the run was cancelled."""


@dataclass
class _Pending:
    confirmation_id: str
    call_id: str
    answer: asyncio.Future[bool | None]


class ActiveRun:
    """A run in progress: its event log and the controls on it."""

    def __init__(self, run_id: str, chat_id: str, user_id: str) -> None:
        self.run_id = run_id
        self.chat_id = chat_id
        self.user_id = user_id
        self.events: list[RunEvent] = []
        self.finished = False
        self.cancel_requested = False
        self.pending: _Pending | None = None
        self.segment: asyncio.Task[Any] | None = None
        self.driver: asyncio.Task[None] | None = None
        self._changed = asyncio.Condition()

    async def publish(self, event: RunEvent) -> None:
        """Append an event and wake streams."""
        async with self._changed:
            self.events.append(event)
            if isinstance(event, RunFinished):
                self.finished = True
            self._changed.notify_all()

    async def stream(self) -> AsyncIterator[RunEvent]:
        """Every event from the first, then live ones, until RunFinished."""
        position = 0
        while True:
            async with self._changed:
                await self._changed.wait_for(lambda seen=position: len(self.events) > seen)
                batch = self.events[position:]
            for event in batch:
                yield event
            position += len(batch)
            if isinstance(batch[-1], RunFinished):
                return

    def request_cancel(self) -> None:
        """Stop the run as soon as possible, wherever it is."""
        self.cancel_requested = True
        if self.pending is not None and not self.pending.answer.done():
            self.pending.answer.set_result(None)
        if self.segment is not None and not self.segment.done():
            self.segment.cancel()


class AgentRuns:
    """The run use cases behind AgentService.SendMessage, Confirm, CancelRun
    and GetRun."""

    def __init__(
        self,
        *,
        services: Services,
        executor: ToolExecutor,
        model_provider: ModelProvider,
        run_store: RunStore,
        chat_store: ChatStore,
        rate_limiter: RateLimiter,
        clock: Clock,
        checkpointer: BaseCheckpointSaver[Any] | None,
        settings: RunSettings | None = None,
        instruments: Instruments | None = None,
    ) -> None:
        self._services = services
        self._executor = executor
        self._models = model_provider
        self._runs = run_store
        self._chats = chat_store
        self._limiter = rate_limiter
        self._clock = clock
        self._settings = settings or RunSettings()
        self._instruments = instruments or Instruments.create()
        self._checkpointer = checkpointer
        self._graph = build_graph(executor, checkpointer)
        self._active: dict[str, ActiveRun] = {}

    async def send_message(self, principal: Principal, chat_id: str, text: str) -> ActiveRun:
        """Start a run for a user message. Errors before the run starts (bad
        chat, rate limit, no permission to run the agent) raise AgentError;
        everything after is reported in the stream."""
        if not text.strip():
            raise AgentError(AgentErrorKind.INVALID_ARGUMENT, "message text is empty")
        chat = await self._chats.get(chat_id)
        if chat is None or chat.user_id != principal.user_id:
            raise AgentError(AgentErrorKind.NOT_FOUND, "chat not found")
        if any(r.chat_id == chat_id and not r.finished for r in self._active.values()):
            raise AgentError(AgentErrorKind.FAILED_PRECONDITION, "a run is already in progress in this chat")
        decision = await self._limiter.check(f"agent-runs:{principal.user_id}")
        if not decision.allowed:
            self._instruments.rate_limited.add(1)
            raise AgentError(
                AgentErrorKind.RATE_LIMITED,
                "too many agent runs; try again later",
                retry_after_s=decision.retry_after_s,
            )
        run_id = uuid7()
        token = await self._issue_token(principal, run_id)
        await self._runs.create(run_id, chat_id, principal.user_id)
        run = ActiveRun(run_id, chat_id, principal.user_id)
        self._active[run_id] = run
        history = [
            HumanMessage(t.text) if t.role == "user" else AIMessage(t.text)
            for t in chat.turns[-2 * self._settings.history_turns :]
        ]
        run.driver = asyncio.create_task(
            self._drive(run, chat.workspace_id, token, [*history, HumanMessage(text)], text),
            name=f"agent-run-{run_id}",
        )
        return run

    async def confirm(self, principal: Principal, run_id: str, confirmation_id: str, approved: bool) -> None:
        """Answer the run's pending confirmation."""
        run = self._owned_active(principal, run_id)
        pending = run.pending
        if pending is None or pending.confirmation_id != confirmation_id or pending.answer.done():
            raise AgentError(AgentErrorKind.FAILED_PRECONDITION, "no such pending confirmation")
        pending.answer.set_result(approved)

    async def cancel(self, principal: Principal, run_id: str) -> None:
        """Cancel a run. Cancelling a finished run is a no-op."""
        run = self._active.get(run_id)
        if run is None:
            trace = await self._runs.get(run_id)
            if trace is None or trace.user_id != principal.user_id:
                raise AgentError(AgentErrorKind.NOT_FOUND, "run not found")
            return
        if run.user_id != principal.user_id:
            raise AgentError(AgentErrorKind.NOT_FOUND, "run not found")
        run.request_cancel()

    async def get_run(self, principal: Principal, run_id: str) -> list[RunEvent]:
        """The run's events, as streamed."""
        trace = await self._runs.get(run_id)
        if trace is None or trace.user_id != principal.user_id:
            raise AgentError(AgentErrorKind.NOT_FOUND, "run not found")
        return trace.events()

    async def shutdown(self) -> None:
        """Cancel every active run and wait for each to record its end."""
        runs = list(self._active.values())
        for run in runs:
            run.request_cancel()
        drivers = [r.driver for r in runs if r.driver is not None]
        if drivers:
            await asyncio.gather(*drivers, return_exceptions=True)

    def _owned_active(self, principal: Principal, run_id: str) -> ActiveRun:
        run = self._active.get(run_id)
        if run is None or run.user_id != principal.user_id:
            raise AgentError(AgentErrorKind.NOT_FOUND, "run not found or already finished")
        return run

    async def _issue_token(self, principal: Principal, run_id: str) -> str:
        s = self._settings
        # The token must outlive the run's working time plus a confirmation wait.
        lifetime = s.budget.max_wall_seconds + s.confirmation_timeout_s + 60
        expires = Timestamp()
        expires.FromMilliseconds(int((self._clock.now() + lifetime) * 1000))
        request = pb.identity_pb2.IssueDelegatedTokenRequest(
            user_id=principal.user_id, agent_run_id=run_id, expires_at=expires
        )
        try:
            response = await self._services.identity.invoke(
                "IssueDelegatedToken", request, token=principal.access_token, timeout_s=s.call_timeout_s
            )
        except ServiceError as err:
            kind = {
                ErrorCode.PERMISSION_DENIED: AgentErrorKind.PERMISSION_DENIED,
                ErrorCode.UNAUTHENTICATED: AgentErrorKind.UNAUTHENTICATED,
            }.get(err.code)
            if kind is None:
                raise
            raise AgentError(kind, "not allowed to run the agent") from err
        return response.access_token

    async def _record(self, run: ActiveRun, item: RunEvent | TraceRecord) -> None:
        await self._runs.append(run.run_id, TraceEntry(self._clock.now() * 1000, item))
        if not isinstance(item, TraceRecord):
            await run.publish(item)

    async def _drive(
        self, run: ActiveRun, workspace_id: str, token: str, messages: list[BaseMessage], text: str
    ) -> None:
        s = self._settings
        budget = BudgetTracker(s.budget, s.pricing)
        run_id_var.set(run.run_id)  # this task's own context copy

        async def emit(item: RunEvent | TraceRecord) -> None:
            await self._record(run, item)

        config = {"configurable": {"thread_id": run.run_id}}
        outcome = Outcome.FAILED
        started = time.monotonic()
        attrs = {"run_id": run.run_id, "chat_id": run.chat_id, "model": self._models.model_name}
        with tracer().start_as_current_span("agent.run", attributes=attrs) as span:
            log.info("run started", extra={"run_id": run.run_id, "chat_id": run.chat_id})
            # Everything after this point, setup included, ends in RunFinished:
            # a stream must never wait for an event that cannot come.
            try:
                await emit(MessageRecord("user", text))
                tool_ctx = ToolRunContext(
                    run_id=run.run_id,
                    user_id=run.user_id,
                    workspace_id=workspace_id,
                    token=token,
                    nonce=os.urandom(8).hex(),
                    sources=SourceRegistry(),
                    services=self._services,
                    deadline=time.monotonic() + s.budget.max_wall_seconds,
                    call_timeout_s=s.call_timeout_s,
                    max_result_chars=s.max_result_chars,
                )
                model = bind_model(self._models.chat_model(), self._executor)
                context = RunContext(tool_ctx, budget, emit, model, SYSTEM_PROMPT)
                outcome = await self._run_graph(
                    run, context, config, {"messages": messages, "decisions": {}, "stop": ""}
                )
                if outcome is Outcome.COMPLETED:
                    await self._finish_completed(run, context, config, len(messages), text)
            except BudgetExceededError as err:
                outcome = Outcome.BUDGET_EXCEEDED
                await emit(NoteRecord(f"budget exceeded: {err.kind.value}"))
            except _Cancelled:
                outcome = Outcome.CANCELLED
            except Exception as err:
                log.exception("run failed", extra={"run_id": run.run_id, "error_type": type(err).__name__})
                await emit(NoteRecord(f"failed: {type(err).__name__}"))
                outcome = Outcome.FAILED
            finally:
                await self._finish(run, budget, outcome, started)
                span.set_attribute("outcome", outcome.name)
                span.set_attribute("steps", budget.steps)

    async def _run_graph(
        self, run: ActiveRun, context: RunContext, config: dict[str, Any], first_input: Any
    ) -> Outcome:
        s = self._settings
        active_seconds = 0.0
        next_input: Any = first_input
        while True:
            remaining = s.budget.max_wall_seconds - active_seconds
            context.tools.deadline = time.monotonic() + remaining
            segment_started = time.monotonic()
            interrupt = await self._segment(run, context, config, next_input, remaining)
            active_seconds += time.monotonic() - segment_started
            if interrupt is None:
                state = await self._graph.aget_state(config)
                return Outcome.CANCELLED if state.values.get("stop") == DECLINED else Outcome.COMPLETED
            approved = await self._ask(run, context, interrupt)
            if approved is None:
                raise _Cancelled()
            next_input = Command(resume={"call_id": interrupt.value["call_id"], "approved": approved})

    async def _segment(
        self, run: ActiveRun, context: RunContext, config: dict[str, Any], graph_input: Any, remaining: float
    ) -> Any:
        """Run the graph until END or an interrupt, in a task CancelRun can
        cancel. Returns the interrupt, or None at END."""

        async def run_until_pause() -> Any:
            async with asyncio.timeout(remaining):
                async for update in self._graph.astream(graph_input, config, context=context, stream_mode="updates"):
                    if "__interrupt__" in update:
                        return update["__interrupt__"][0]
            return None

        if run.cancel_requested:
            raise _Cancelled()
        run.segment = asyncio.create_task(run_until_pause())
        try:
            await asyncio.wait({run.segment})
        except asyncio.CancelledError:
            # The driver itself is being cancelled (shutdown): stop the segment too.
            run.segment.cancel()
            raise
        if run.cancel_requested:
            raise _Cancelled()
        try:
            return run.segment.result()
        except TimeoutError:
            context.budget.mark_wall_time_exceeded()
            raise BudgetExceededError(BudgetKind.WALL_TIME) from None
        finally:
            run.segment = None

    async def _ask(self, run: ActiveRun, context: RunContext, interrupt: Any) -> bool | None:
        """Emit the confirmation request and wait for the answer. None means
        cancelled or timed out."""
        value = interrupt.value
        pending = _Pending(interrupt.id, value["call_id"], asyncio.get_running_loop().create_future())
        run.pending = pending
        await context.emit(
            ConfirmationRequested(interrupt.id, value["description"], value["tool"], value["arguments_json"])
        )
        if run.cancel_requested:
            return None
        try:
            answer = await asyncio.wait_for(pending.answer, self._settings.confirmation_timeout_s)
        except TimeoutError:
            await context.emit(NoteRecord("confirmation timed out"))
            answer = None
        finally:
            run.pending = None
        label = "timeout_or_cancel" if answer is None else ("approved" if answer else "declined")
        self._instruments.confirmations.add(1, {"tool": value["tool"], "answer": label})
        return answer

    async def _finish_completed(
        self, run: ActiveRun, context: RunContext, config: dict[str, Any], input_count: int, user_text: str
    ) -> None:
        state = await self._graph.aget_state(config)
        produced = [m for m in state.values["messages"][input_count:] if isinstance(m, AIMessage)]
        answer = message_text(produced[-1].content) if produced else ""
        check = extract_citations("\n".join(message_text(m.content) for m in produced), context.tools.sources)
        for source in check.cited:
            await context.emit(CitationAdded(source.note_id, source.block_id, source.start, source.end, source.quote))
        if check.unknown_labels:
            await context.emit(NoteRecord(f"citations without a retrieved source: {', '.join(check.unknown_labels)}"))
        await context.emit(MessageRecord("assistant", answer))
        await self._chats.append_turns(
            run.chat_id, [ChatTurn("user", user_text), ChatTurn("assistant", answer)], int(self._clock.now() * 1000)
        )

    async def _finish(self, run: ActiveRun, budget: BudgetTracker, outcome: Outcome, started: float) -> None:
        finished = RunFinished(outcome, budget.steps, budget.input_tokens, budget.output_tokens)
        try:
            await self._record(run, finished)
        finally:
            self._active.pop(run.run_id, None)
        # A finished run is never resumed; its checkpoints would only grow
        # the store. The trace keeps everything worth keeping.
        if self._checkpointer is not None:
            try:
                await self._checkpointer.adelete_thread(run.run_id)
            except Exception as err:
                log.warning(
                    "could not delete run checkpoints", extra={"run_id": run.run_id, "error_type": type(err).__name__}
                )
        elapsed = time.monotonic() - started
        attrs = {"outcome": outcome.name.lower()}
        self._instruments.runs.add(1, attrs)
        self._instruments.steps.record(budget.steps, attrs)
        self._instruments.run_latency.record(elapsed, attrs)
        self._instruments.tokens.add(budget.input_tokens, {"direction": "input"})
        self._instruments.tokens.add(budget.output_tokens, {"direction": "output"})
        self._instruments.cost.add(budget.cost_usd)
        log.info(
            "run finished",
            extra={
                "run_id": run.run_id,
                "outcome": outcome.name,
                "steps": budget.steps,
                "input_tokens": budget.input_tokens,
                "output_tokens": budget.output_tokens,
                "duration_ms": round(elapsed * 1000, 1),
            },
        )


def model_calls(records: Sequence[TraceRecord]) -> int:
    """How many model calls a trace records."""
    return sum(1 for r in records if isinstance(r, ModelCallRecord))
