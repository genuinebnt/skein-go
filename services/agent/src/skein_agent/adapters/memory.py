"""In-memory RunStore and ChatStore.

Used by the tests, the evals and ``SKEIN_AGENT_STORAGE=memory``; production
uses the Postgres stores (``adapters/postgres.py``), which implement the
same contract and pass the same tests (``tests/test_stores.py``). Safe under
concurrent coroutines because no method awaits while it reads or changes
state, so each compare-and-set in the RunStore contract is atomic here.
Traces of finished runs are kept for the most recent ``max_runs`` runs;
unfinished runs are never evicted.
"""

from __future__ import annotations

from collections import OrderedDict
from collections.abc import Sequence
from dataclasses import dataclass

from ..app.ports import Chat, ChatTurn, PausedRun, RunInfo, chat_title
from ..domain.budget import RunUsage
from ..domain.events import RunStatus, RunTrace, TraceEntry


@dataclass
class _Run:
    info: RunInfo
    trace: RunTrace
    running_since_ms: int
    paused: PausedRun | None = None


class InMemoryRunStore:
    """Runs in a dict, finished ones bounded as an LRU."""

    def __init__(self, max_runs: int = 10_000) -> None:
        self._max = max_runs
        self._runs: OrderedDict[str, _Run] = OrderedDict()

    async def create(self, run: RunInfo, at_ms: int) -> bool:
        """See RunStore.create."""
        if any(r.info.chat_id == run.chat_id and r.trace.status is not RunStatus.FINISHED for r in self._runs.values()):
            return False
        self._runs[run.run_id] = _Run(run, RunTrace(run.run_id, run.chat_id, run.user_id), at_ms)
        finished = [k for k, r in self._runs.items() if r.trace.status is RunStatus.FINISHED]
        for run_id in finished[: max(0, len(self._runs) - self._max)]:
            del self._runs[run_id]
        return True

    async def append(self, run_id: str, entry: TraceEntry) -> None:
        """See RunStore.append."""
        run = self._runs.get(run_id)
        if run is not None:
            run.trace.entries.append(entry)

    async def get(self, run_id: str) -> RunTrace | None:
        """See RunStore.get. The trace is a copy, as from a database."""
        run = self._runs.get(run_id)
        if run is None:
            return None
        t = run.trace
        return RunTrace(t.run_id, t.chat_id, t.user_id, list(t.entries), t.status, t.usage)

    async def pause(self, paused: PausedRun) -> bool:
        """See RunStore.pause."""
        run = self._runs.get(paused.run.run_id)
        if run is None or run.trace.status is not RunStatus.RUNNING:
            return False
        run.trace.status, run.paused = RunStatus.PAUSED, paused
        return True

    async def claim_paused(self, run_id: str, confirmation_id: str, at_ms: int) -> PausedRun | None:
        """See RunStore.claim_paused."""
        run = self._runs.get(run_id)
        if run is None or run.trace.status is not RunStatus.PAUSED or run.paused is None:
            return None
        if confirmation_id and run.paused.confirmation_id != confirmation_id:
            return None
        paused, run.paused = run.paused, None
        run.trace.status, run.running_since_ms = RunStatus.RUNNING, at_ms
        return paused

    async def claim_stale(self, run_id: str, running_before_ms: int, at_ms: int) -> bool:
        """See RunStore.claim_stale."""
        run = self._runs.get(run_id)
        if run is None or run.trace.status is not RunStatus.RUNNING or run.running_since_ms >= running_before_ms:
            return False
        run.running_since_ms = at_ms
        return True

    async def stale(self, *, paused_before_ms: int, running_before_ms: int) -> Sequence[str]:
        """See RunStore.stale."""

        def is_stale(run: _Run) -> bool:
            if run.trace.status is RunStatus.PAUSED:
                return run.paused is not None and run.paused.paused_at_ms < paused_before_ms
            return run.trace.status is RunStatus.RUNNING and run.running_since_ms < running_before_ms

        return [run_id for run_id, run in self._runs.items() if is_stale(run)]

    async def finish(self, run_id: str, usage: RunUsage) -> None:
        """See RunStore.finish."""
        run = self._runs.get(run_id)
        if run is not None:
            run.trace.status, run.trace.usage, run.paused = RunStatus.FINISHED, usage, None
            self._runs.move_to_end(run_id)


class InMemoryChatStore:
    """Chats and transcripts."""

    def __init__(self) -> None:
        self._chats: dict[str, Chat] = {}

    async def create(self, chat: Chat) -> None:
        """See ChatStore.create."""
        self._chats[chat.chat_id] = chat

    async def get(self, chat_id: str) -> Chat | None:
        """See ChatStore.get."""
        return self._chats.get(chat_id)

    async def list(self, user_id: str, workspace_id: str, *, limit: int, after: str) -> Sequence[Chat]:
        """See ChatStore.list."""
        chats = sorted(
            (c for c in self._chats.values() if c.user_id == user_id and c.workspace_id == workspace_id),
            key=lambda c: (-c.updated_at_ms, c.chat_id),
        )
        if after:
            ids = [c.chat_id for c in chats]
            chats = chats[ids.index(after) + 1 :] if after in ids else []
        return chats[:limit]

    async def append_turns(self, chat_id: str, turns: Sequence[ChatTurn], at_ms: int) -> None:
        """See ChatStore.append_turns."""
        chat = self._chats.get(chat_id)
        if chat is None:
            return
        chat.turns.extend(turns)
        chat.updated_at_ms = at_ms
        if not chat.title:
            chat.title = chat_title(chat.turns)
