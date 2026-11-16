"""In-memory RunStore and ChatStore.

These are the production stores for now (the ``agent`` Postgres schema is a
follow-up), so they are adapters rather than fakes: bounded, and safe under
concurrent coroutines because every method completes without awaiting.
Traces are kept for the most recent ``max_runs`` runs.
"""

from __future__ import annotations

from collections import OrderedDict
from collections.abc import Sequence

from ..app.ports import Chat, ChatTurn
from ..domain.events import RunTrace, TraceEntry

_TITLE_CHARS = 60


class InMemoryRunStore:
    """Run traces in a bounded LRU."""

    def __init__(self, max_runs: int = 10_000) -> None:
        self._max = max_runs
        self._traces: OrderedDict[str, RunTrace] = OrderedDict()

    async def create(self, run_id: str, chat_id: str, user_id: str) -> None:
        """See RunStore.create."""
        self._traces[run_id] = RunTrace(run_id, chat_id, user_id)
        while len(self._traces) > self._max:
            self._traces.popitem(last=False)

    async def append(self, run_id: str, entry: TraceEntry) -> None:
        """See RunStore.append."""
        trace = self._traces.get(run_id)
        if trace is not None:
            trace.entries.append(entry)

    async def get(self, run_id: str) -> RunTrace | None:
        """See RunStore.get."""
        return self._traces.get(run_id)


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
            first = next((t.text for t in chat.turns if t.role == "user"), "")
            chat.title = first if len(first) <= _TITLE_CHARS else first[: _TITLE_CHARS - 1] + "…"
