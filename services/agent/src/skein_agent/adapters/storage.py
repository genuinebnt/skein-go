"""Storage selection: Postgres (the default) or in memory, for all three of
chats, runs and the LangGraph checkpointer together.

They are chosen together because a paused run needs all three to resume:
the RunStore's paused state, the checkpointer's graph state and the chat
transcript. In memory (``SKEIN_AGENT_STORAGE=memory``) is for local runs
and tests; nothing survives the process, so a paused run does not either.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass
from typing import Any

from langgraph.checkpoint.base import BaseCheckpointSaver
from langgraph.checkpoint.memory import InMemorySaver

from ..app.ports import ChatStore, RunStore
from .memory import InMemoryChatStore, InMemoryRunStore


@dataclass(frozen=True)
class Storage:
    """Where runs, chats and graph checkpoints are kept."""

    runs: RunStore
    chats: ChatStore
    checkpointer: BaseCheckpointSaver[Any]
    durable: bool


@asynccontextmanager
async def open_storage(kind: str, postgres_dsn: str = "") -> AsyncIterator[Storage]:
    """Yield the storage for ``kind`` ("postgres" or "memory")."""
    if kind == "memory":
        yield Storage(InMemoryRunStore(), InMemoryChatStore(), InMemorySaver(), durable=False)
        return
    if kind != "postgres":
        raise ValueError(f"unknown storage {kind!r}; use postgres or memory")
    if not postgres_dsn:
        raise ValueError("SKEIN_AGENT_POSTGRES_DSN is required for postgres storage")
    from .postgres import open_postgres

    async with open_postgres(postgres_dsn) as pg:
        yield Storage(pg.runs, pg.chats, pg.checkpointer, durable=True)
