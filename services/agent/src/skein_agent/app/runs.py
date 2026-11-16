"""Agent runs: starting, streaming, confirming, cancelling, tracing.

A run answers one user message. ``AgentRuns.send_message`` checks the chat
and the user's rate limit, gets a delegated token for (user, run) from
identity (ADR-0009), records the run (one unfinished run per chat), and
starts a driver task. The caller streams the run's events; the driver keeps
going while it waits for a confirmation, because Confirm arrives on another
RPC.

Driver loop::

    segment: run the graph until it ends or interrupts
      interrupt → store the pause, emit ConfirmationRequest, wait for
                  Confirm (or cancel, or timeout), resume the graph
      end       → emit citations, store the transcript
    always: emit RunFinished with the outcome and usage

Outcomes:

- COMPLETED: the graph reached END normally.
- CANCELLED: CancelRun, the user declined a confirmation, the confirmation
  timed out, or the service shut down while the run was working. A client
  leaving the stream cancels nothing: the run goes on and GetRun reads it.
- BUDGET_EXCEEDED: no step left (steps, tokens, cost), or the wall-time
  deadline fired. Waiting for the user does not count against wall time.
- FAILED: anything else (logged by error type, never with content), and
  runs whose process stopped mid-segment (found by ``expire_stale``).

**Paused runs are durable.** A pause stores a ``PausedRun`` (everything but
the graph state, which the checkpointer holds under thread ID = run ID), so
a paused run survives its process: shutdown leaves it paused. Confirm,
CancelRun and the timeout each first *claim* the pause in the RunStore, and
only the claimant acts; that makes the outcome the same whether the pause
is live in this process, was left by a process that restarted, or is live
on another replica. Confirm for a pause not live here resumes the run in
this process, with a delegated token obtained from the confirming user's own
token (tokens are never stored). The loser of a claim race, if it is the
original driver, lets go of the run without finishing it; its stream ends
early and the client reads the rest with GetRun.

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
from langchain_core.messages import AIMessage, HumanMessage
from langgraph.checkpoint.base import BaseCheckpointSaver
from langgraph.types import Command

from .. import pb
from ..domain.budget import Budget, BudgetExceededError, BudgetKind, BudgetTracker, Pricing, RunUsage
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
    RunStatus,
    RunTrace,
    TraceEntry,
    TraceRecord,
)
from ..domain.ids import uuid7
from ..domain.untrusted import SourceRegistry, extract_citations
from ..observability import Instruments, run_id_var, tracer
from .graph import DECLINED, RunContext, bind_model, build_graph, message_text
from .ports import ChatStore, ChatTurn, Clock, ModelProvider, PausedRun, RateLimiter, RunInfo, RunStore, Services
from .prompts import SYSTEM_PROMPT
from .tools.base import RunToolState, ToolRunContext
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


# A run left running this much longer than its wall-time budget belongs to a
# process that stopped: no live driver runs a segment past its deadline.
STALE_GRACE_S = 60.0


class _Cancelled(Exception):
    """Internal: the run was cancelled."""


class _LetGo(Exception):
    """Internal: this driver no longer owns the run. It stays paused in the
    store (shutdown), or another process claimed its pause; either way this
    driver must not finish it."""


@dataclass
class _Pending:
    """A confirmation this process is waiting on.

    ``taken`` is set, without awaiting, by the first of Confirm, cancel,
    shutdown or the timeout to act on it; only that one may claim the pause
    in the store, so two local actors never race each other there.
    ``claimed_elsewhere`` means Confirm found the pause already claimed by
    another process.
    """

    confirmation_id: str
    call_id: str
    tool: str
    answer: asyncio.Future[bool | None]
    taken: bool = False
    claimed_elsewhere: bool = False

    def take(self, answer: bool | None = None, *, resolve: bool = True) -> bool:
        """Take the confirmation if nobody has; resolve the wait with
        ``answer`` unless ``resolve`` is False. Returns whether it was taken."""
        if self.taken:
            return False
        self.taken = True
        if resolve:
            self.answer.set_result(answer)
        return True


@dataclass
class _Progress:
    """What the driver carries across pauses besides the graph state."""

    workspace_id: str
    user_text: str
    input_count: int
    active_seconds: float = 0.0


class ActiveRun:
    """A run driven by this process: its event log and the controls on it."""

    def __init__(self, run_id: str, chat_id: str, user_id: str) -> None:
        self.run_id = run_id
        self.chat_id = chat_id
        self.user_id = user_id
        self.events: list[RunEvent] = []
        self.finished = False
        self.released = False
        self.cancel_requested = False
        self.pending: _Pending | None = None
        self.segment: asyncio.Task[Any] | None = None
        self.driver: asyncio.Task[None] | None = None
        self._changed = asyncio.Condition()

    @property
    def paused(self) -> bool:
        """Waiting for an answer to a confirmation nobody has acted on yet."""
        return self.pending is not None and not self.pending.taken

    async def publish(self, event: RunEvent) -> None:
        """Append an event and wake streams."""
        async with self._changed:
            self.events.append(event)
            if isinstance(event, RunFinished):
                self.finished = True
            self._changed.notify_all()

    async def stream(self) -> AsyncIterator[RunEvent]:
        """Every event from the first, then live ones, until RunFinished, or
        until this process lets go of the run (then without RunFinished)."""
        position = 0
        while True:
            async with self._changed:
                await self._changed.wait_for(lambda seen=position: len(self.events) > seen or self.released)
                batch = self.events[position:]
            for event in batch:
                yield event
            position += len(batch)
            if (batch and isinstance(batch[-1], RunFinished)) or (not batch and self.released):
                return

    async def release(self) -> None:
        """Stop driving the run without finishing it, and end streams."""
        async with self._changed:
            self.released = True
            self._changed.notify_all()
        if self.pending is not None:
            self.pending.take()

    def request_cancel(self) -> None:
        """Stop the run as soon as possible, wherever it is."""
        if self.released:
            return
        self.cancel_requested = True
        if self.pending is not None:
            self.pending.take()
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
        if not await self._runs.create(RunInfo(run_id, chat_id, principal.user_id, chat.workspace_id), self._now_ms()):
            raise AgentError(AgentErrorKind.FAILED_PRECONDITION, "a run is already in progress in this chat")
        run = self._register(run_id, chat_id, principal.user_id)
        history = [
            HumanMessage(t.text) if t.role == "user" else AIMessage(t.text)
            for t in chat.turns[-2 * self._settings.history_turns :]
        ]
        messages = [*history, HumanMessage(text)]
        progress = _Progress(chat.workspace_id, text, len(messages))
        budget = BudgetTracker(self._settings.budget, self._settings.pricing)
        tools = self._tool_context(run, token, progress.workspace_id, os.urandom(8).hex(), SourceRegistry(), 0)
        graph_input = {"messages": messages, "decisions": {}, "stop": ""}
        run.driver = asyncio.create_task(
            self._drive(run, progress, budget, tools, graph_input, resumed=False), name=f"agent-run-{run_id}"
        )
        return run

    async def confirm(self, principal: Principal, run_id: str, confirmation_id: str, approved: bool) -> None:
        """Answer a run's pending confirmation, live in this process or not."""
        run = self._active.get(run_id)
        if run is not None:
            if run.user_id != principal.user_id:
                raise AgentError(AgentErrorKind.NOT_FOUND, "run not found or already finished")
            pending = run.pending
            if pending is None or pending.confirmation_id != confirmation_id or not pending.take(resolve=False):
                raise AgentError(AgentErrorKind.FAILED_PRECONDITION, "no such pending confirmation")
            if await self._runs.claim_paused(run_id, confirmation_id, self._now_ms()) is None:
                pending.claimed_elsewhere = True
                pending.answer.set_result(None)
                raise AgentError(AgentErrorKind.FAILED_PRECONDITION, "the confirmation was already answered")
            self._count_answer(pending.tool, approved)
            pending.answer.set_result(approved)
            return
        trace = await self._runs.get(run_id)
        if trace is None or trace.user_id != principal.user_id:
            raise AgentError(AgentErrorKind.NOT_FOUND, "run not found or already finished")
        paused = await self._runs.claim_paused(run_id, confirmation_id, self._now_ms())
        if paused is None:
            raise AgentError(AgentErrorKind.FAILED_PRECONDITION, "no such pending confirmation")
        if self._now_ms() - paused.paused_at_ms > self._settings.confirmation_timeout_s * 1000:
            await self._end_unattended(paused.run.run_id, paused.usage, Outcome.CANCELLED, "confirmation timed out")
            raise AgentError(AgentErrorKind.FAILED_PRECONDITION, "the confirmation timed out")
        self._count_answer(paused.tool, approved)
        await self._resume(principal, paused, approved)

    async def cancel(self, principal: Principal, run_id: str) -> None:
        """Cancel a run. Cancelling a finished run is a no-op."""
        run = self._active.get(run_id)
        if run is not None:
            if run.user_id != principal.user_id:
                raise AgentError(AgentErrorKind.NOT_FOUND, "run not found")
            run.request_cancel()
            return
        trace = await self._runs.get(run_id)
        if trace is None or trace.user_id != principal.user_id:
            raise AgentError(AgentErrorKind.NOT_FOUND, "run not found")
        if trace.status is RunStatus.PAUSED:
            paused = await self._runs.claim_paused(run_id, "", self._now_ms())
            if paused is not None:
                await self._end_unattended(run_id, paused.usage, Outcome.CANCELLED, "")
        # A run running on another replica stops at its deadline; the
        # contract has no way to reach that replica (a follow-up).

    async def get_run(self, principal: Principal, run_id: str) -> RunTrace:
        """The run's stored trace: its events as streamed (``events()``),
        status and usage."""
        trace = await self._runs.get(run_id)
        if trace is None or trace.user_id != principal.user_id:
            raise AgentError(AgentErrorKind.NOT_FOUND, "run not found")
        return trace

    async def expire_stale(self) -> int:
        """End runs no process will finish: pauses past the confirmation
        timeout (as CANCELLED) and runs left running by a stopped process (as
        FAILED). Safe to call from every replica; returns how many it ended."""
        now = self._now_ms()
        s = self._settings
        paused_before = now - int(s.confirmation_timeout_s * 1000)
        running_before = now - int((s.budget.max_wall_seconds + STALE_GRACE_S) * 1000)
        ended = 0
        for run_id in await self._runs.stale(paused_before_ms=paused_before, running_before_ms=running_before):
            if run_id in self._active:
                continue  # its driver has its own timeout
            paused = await self._runs.claim_paused(run_id, "", now)
            if paused is not None and paused.paused_at_ms < paused_before:
                await self._end_unattended(run_id, paused.usage, Outcome.CANCELLED, "confirmation timed out")
                ended += 1
            elif paused is None and await self._runs.claim_stale(run_id, running_before, now):
                trace = await self._runs.get(run_id)
                usage = trace.usage_so_far() if trace is not None else RunUsage()
                await self._end_unattended(run_id, usage, Outcome.FAILED, "abandoned: the process running it stopped")
                ended += 1
        return ended

    async def shutdown(self) -> None:
        """Stop driving every active run: paused ones stay paused in the
        store (any process can resume them), the rest are cancelled and
        record their end."""
        runs = list(self._active.values())
        for run in runs:
            if run.paused:
                await run.release()
            else:
                run.request_cancel()
        drivers = [r.driver for r in runs if r.driver is not None]
        if drivers:
            await asyncio.gather(*drivers, return_exceptions=True)

    def _register(self, run_id: str, chat_id: str, user_id: str) -> ActiveRun:
        run = ActiveRun(run_id, chat_id, user_id)
        self._active[run_id] = run
        return run

    def _now_ms(self) -> int:
        return int(self._clock.now() * 1000)

    def _count_answer(self, tool: str, approved: bool | None) -> None:
        label = "timeout_or_cancel" if approved is None else ("approved" if approved else "declined")
        self._instruments.confirmations.add(1, {"tool": tool, "answer": label})

    def _tool_context(
        self, run: ActiveRun, token: str, workspace_id: str, nonce: str, sources: SourceRegistry, replica_id: int
    ) -> ToolRunContext:
        s = self._settings
        return ToolRunContext(
            run_id=run.run_id,
            user_id=run.user_id,
            workspace_id=workspace_id,
            token=token,
            nonce=nonce,
            sources=sources,
            services=self._services,
            deadline=time.monotonic() + s.budget.max_wall_seconds,
            call_timeout_s=s.call_timeout_s,
            max_result_chars=s.max_result_chars,
            state=RunToolState(replica_id),
        )

    async def _resume(self, principal: Principal, paused: PausedRun, approved: bool) -> None:
        """Drive a run whose pause this process has just claimed."""
        info = paused.run
        try:
            token = await self._issue_token(principal, info.run_id)
        except Exception as err:
            # The pause is claimed, so nobody else will end the run.
            note = f"resume failed: {type(err).__name__}"
            await self._end_unattended(info.run_id, paused.usage, Outcome.FAILED, note)
            raise
        run = self._register(info.run_id, info.chat_id, info.user_id)
        progress = _Progress(info.workspace_id, paused.user_text, paused.input_count, paused.active_seconds)
        budget = BudgetTracker.resumed(self._settings.budget, self._settings.pricing, paused.usage)
        sources = SourceRegistry.restore(paused.sources)
        tools = self._tool_context(run, token, info.workspace_id, paused.nonce, sources, paused.replica_id)
        resume = Command(resume={"call_id": paused.call_id, "approved": approved})
        run.driver = asyncio.create_task(
            self._drive(run, progress, budget, tools, resume, resumed=True), name=f"agent-run-{info.run_id}"
        )

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
        self,
        run: ActiveRun,
        progress: _Progress,
        budget: BudgetTracker,
        tools: ToolRunContext,
        graph_input: Any,
        *,
        resumed: bool,
    ) -> None:
        run_id_var.set(run.run_id)  # this task's own context copy

        async def emit(item: RunEvent | TraceRecord) -> None:
            await self._record(run, item)

        config = {"configurable": {"thread_id": run.run_id}}
        outcome = Outcome.FAILED
        let_go = False
        started = time.monotonic()
        attrs = {"run_id": run.run_id, "chat_id": run.chat_id, "model": self._models.model_name, "resumed": resumed}
        with tracer().start_as_current_span("agent.run", attributes=attrs) as span:
            log.info("run resumed" if resumed else "run started", extra={"run_id": run.run_id, "chat_id": run.chat_id})
            # Everything after this point, setup included, ends in RunFinished
            # (unless the run is let go): a stream must never wait for an
            # event that cannot come.
            try:
                if not resumed:
                    await emit(MessageRecord("user", progress.user_text))
                model = bind_model(self._models.chat_model(), self._executor)
                context = RunContext(tools, budget, emit, model, SYSTEM_PROMPT)
                outcome = await self._run_graph(run, context, config, graph_input, progress)
                if outcome is Outcome.COMPLETED:
                    await self._finish_completed(run, context, config, progress)
            except BudgetExceededError as err:
                outcome = Outcome.BUDGET_EXCEEDED
                await emit(NoteRecord(f"budget exceeded: {err.kind.value}"))
            except _Cancelled:
                outcome = Outcome.CANCELLED
            except _LetGo:
                let_go = True
            except Exception as err:
                log.exception("run failed", extra={"run_id": run.run_id, "error_type": type(err).__name__})
                await emit(NoteRecord(f"failed: {type(err).__name__}"))
                outcome = Outcome.FAILED
            finally:
                if let_go:
                    self._active.pop(run.run_id, None)
                    await run.release()
                    log.info("run let go while paused", extra={"run_id": run.run_id})
                    span.set_attribute("outcome", "let_go")
                else:
                    await self._finish(run, budget, outcome, started)
                    span.set_attribute("outcome", outcome.name)
                span.set_attribute("steps", budget.steps)

    async def _run_graph(
        self, run: ActiveRun, context: RunContext, config: dict[str, Any], first_input: Any, progress: _Progress
    ) -> Outcome:
        s = self._settings
        next_input: Any = first_input
        while True:
            remaining = s.budget.max_wall_seconds - progress.active_seconds
            context.tools.deadline = time.monotonic() + remaining
            segment_started = time.monotonic()
            interrupt = await self._segment(run, context, config, next_input, remaining)
            progress.active_seconds += time.monotonic() - segment_started
            if interrupt is None:
                state = await self._graph.aget_state(config)
                return Outcome.CANCELLED if state.values.get("stop") == DECLINED else Outcome.COMPLETED
            approved = await self._ask(run, context, interrupt, progress)
            next_input = Command(resume={"call_id": interrupt.value["call_id"], "approved": approved})

    async def _segment(
        self, run: ActiveRun, context: RunContext, config: dict[str, Any], graph_input: Any, remaining: float
    ) -> Any:
        """Run the graph until END or an interrupt, in a task CancelRun can
        cancel. Returns the interrupt, or None at END."""

        async def run_until_pause() -> Any:
            # The stream runs to its end rather than returning at the
            # interrupt, and checkpoints are written synchronously: the pause
            # is stored right after this, and another process may resume
            # from the checkpoint at once.
            pause = None
            async with asyncio.timeout(remaining):
                async for update in self._graph.astream(
                    graph_input, config, context=context, stream_mode="updates", durability="sync"
                ):
                    if "__interrupt__" in update:
                        pause = update["__interrupt__"][0]
            return pause

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

    async def _ask(self, run: ActiveRun, context: RunContext, interrupt: Any, progress: _Progress) -> bool:
        """Store the pause, emit the confirmation request and wait for the
        answer. Raises _Cancelled (cancel, timeout) or _LetGo."""
        value = interrupt.value
        tools = context.tools
        paused = PausedRun(
            run=RunInfo(run.run_id, run.chat_id, run.user_id, tools.workspace_id),
            user_text=progress.user_text,
            input_count=progress.input_count,
            confirmation_id=interrupt.id,
            call_id=value["call_id"],
            tool=value["tool"],
            nonce=tools.nonce,
            sources=tools.sources.sources(),
            replica_id=tools.state.replica_id,
            usage=context.budget.usage(),
            active_seconds=progress.active_seconds,
            paused_at_ms=self._now_ms(),
        )
        pending = _Pending(interrupt.id, value["call_id"], value["tool"], asyncio.get_running_loop().create_future())
        try:
            # Stored before the request is shown, so any answer finds it.
            if not await self._runs.pause(paused):
                raise _LetGo()
            run.pending = pending
            if run.cancel_requested:
                pending.take()
            await context.emit(
                ConfirmationRequested(interrupt.id, value["description"], value["tool"], value["arguments_json"])
            )
            timed_out = False
            try:
                # Shielded: a timeout must not cancel a future Confirm may still resolve.
                answer = await asyncio.wait_for(asyncio.shield(pending.answer), self._settings.confirmation_timeout_s)
            except TimeoutError:
                timed_out = pending.take(resolve=False)
                answer = None if timed_out else await pending.answer  # Confirm is mid-claim; its result decides
        finally:
            run.pending = None
        if answer is not None:
            return answer  # Confirm claimed the pause before answering
        if run.released or pending.claimed_elsewhere:
            raise _LetGo()
        # Cancelled or timed out here; another process may have claimed it first.
        if await self._runs.claim_paused(run.run_id, interrupt.id, self._now_ms()) is None:
            raise _LetGo()
        if timed_out:
            await context.emit(NoteRecord("confirmation timed out"))
        self._count_answer(value["tool"], None)
        raise _Cancelled()

    async def _finish_completed(
        self, run: ActiveRun, context: RunContext, config: dict[str, Any], progress: _Progress
    ) -> None:
        state = await self._graph.aget_state(config)
        produced = [m for m in state.values["messages"][progress.input_count :] if isinstance(m, AIMessage)]
        answer = message_text(produced[-1].content) if produced else ""
        check = extract_citations("\n".join(message_text(m.content) for m in produced), context.tools.sources)
        for source in check.cited:
            await context.emit(CitationAdded(source.note_id, source.block_id, source.start, source.end, source.quote))
        if check.unknown_labels:
            await context.emit(NoteRecord(f"citations without a retrieved source: {', '.join(check.unknown_labels)}"))
        await context.emit(MessageRecord("assistant", answer))
        await self._chats.append_turns(
            run.chat_id,
            [ChatTurn("user", progress.user_text), ChatTurn("assistant", answer)],
            self._now_ms(),
        )

    async def _end_unattended(self, run_id: str, usage: RunUsage, outcome: Outcome, note: str) -> None:
        """Finish a run this process has claimed but is not driving."""
        now = self._clock.now() * 1000
        if note:
            await self._runs.append(run_id, TraceEntry(now, NoteRecord(note)))
        finished = RunFinished(outcome, usage.steps, usage.input_tokens, usage.output_tokens)
        await self._runs.append(run_id, TraceEntry(now, finished))
        await self._runs.finish(run_id, usage)
        await self._forget_checkpoints(run_id)
        self._instruments.runs.add(1, {"outcome": outcome.name.lower()})
        log.info("run ended unattended", extra={"run_id": run_id, "outcome": outcome.name})

    async def _forget_checkpoints(self, run_id: str) -> None:
        # A finished run is never resumed; its checkpoints would only grow
        # the store. The trace keeps everything worth keeping.
        if self._checkpointer is None:
            return
        try:
            await self._checkpointer.adelete_thread(run_id)
        except Exception as err:
            log.warning("could not delete run checkpoints", extra={"run_id": run_id, "error_type": type(err).__name__})

    async def _finish(self, run: ActiveRun, budget: BudgetTracker, outcome: Outcome, started: float) -> None:
        finished = RunFinished(outcome, budget.steps, budget.input_tokens, budget.output_tokens)
        try:
            await self._record(run, finished)
            await self._runs.finish(run.run_id, budget.usage())
        finally:
            self._active.pop(run.run_id, None)
        await self._forget_checkpoints(run.run_id)
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
