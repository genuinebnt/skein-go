"""LangGraph checkpointer selection: in memory, or Postgres (optional).

The checkpointer holds a run's graph state between a confirmation request
and the answer. In memory is enough while runs live in one process; the
Postgres saver (``langgraph-checkpoint-postgres``, the ``postgres`` extra)
keeps that state across restarts, which is the first step towards resuming
paused runs on another replica (a follow-up: the driver loop itself is
still in-process).
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any

from langgraph.checkpoint.base import BaseCheckpointSaver
from langgraph.checkpoint.memory import InMemorySaver


@asynccontextmanager
async def open_checkpointer(kind: str, postgres_dsn: str = "") -> AsyncIterator[BaseCheckpointSaver[Any]]:
    """Yield a checkpointer for ``kind`` ("memory" or "postgres")."""
    if kind == "memory":
        yield InMemorySaver()
        return
    if kind != "postgres":
        raise ValueError(f"unknown checkpointer {kind!r}; use memory or postgres")
    if not postgres_dsn:
        raise ValueError("SKEIN_AGENT_POSTGRES_DSN is required for the postgres checkpointer")
    try:
        from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver
    except ImportError as err:
        raise RuntimeError("install the 'postgres' extra for the Postgres checkpointer") from err
    async with AsyncPostgresSaver.from_conn_string(postgres_dsn) as saver:
        await saver.setup()  # idempotent: creates its tables on first use
        yield saver
