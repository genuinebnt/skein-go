"""Postgres RunStore and ChatStore, and the storage they share with the
LangGraph checkpointer (the ``postgres`` extra).

Everything lives in the ``agent`` schema (``migrations/``). One
``psycopg_pool.AsyncConnectionPool`` serves both stores and
``AsyncPostgresSaver``; its connections use ``search_path=agent`` so the
saver's own tables land in the schema too, and autocommit with dict rows,
which the saver requires. Methods that read and write together run in one
transaction; the RunStore's status changes are single ``UPDATE ... WHERE
status = ...`` statements, so the compare-and-set the port promises is the
database's row lock, across every replica.

``open_postgres`` applies pending migrations (recorded in
``agent.schema_migrations``) and the saver's setup under a session advisory
lock, so replicas starting together do not race. Integration tests:
``tests/test_stores.py`` and ``tests/test_restart.py`` with ``-m integration``.
"""

from __future__ import annotations

import asyncio
import os
from collections.abc import AsyncIterator, Sequence
from contextlib import asynccontextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from ..app.ports import Chat, ChatTurn, PausedRun, RunInfo, chat_title
from ..domain.budget import RunUsage
from ..domain.events import RunStatus, RunTrace, TraceEntry
from .codec import item_from_json, item_to_json, paused_from_json, paused_to_json

# The source tree's migrations/; the image points SKEIN_AGENT_MIGRATIONS_DIR at its copy.
MIGRATIONS = Path(os.environ.get("SKEIN_AGENT_MIGRATIONS_DIR") or Path(__file__).resolve().parents[3] / "migrations")
_LOCK_KEY = "skein-agent-migrations"


def _jsonb(value: Any) -> Any:
    from psycopg.types.json import Jsonb

    return Jsonb(value)


def _read_migrations(directory: Path) -> list[tuple[str, str]]:
    return [(path.name, path.read_text()) for path in sorted(directory.glob("*.sql"))]


async def migrate(conn: Any, directory: Path = MIGRATIONS) -> list[str]:
    """Apply pending ``*.sql`` files in name order; returns those applied.
    The caller holds the migration lock."""
    files = await asyncio.to_thread(_read_migrations, directory)
    applied = []
    async with conn.transaction():
        await conn.execute("CREATE SCHEMA IF NOT EXISTS agent")
        await conn.execute(
            "CREATE TABLE IF NOT EXISTS agent.schema_migrations"
            " (name text PRIMARY KEY, applied_at timestamptz DEFAULT now())"
        )
        done = {r["name"] for r in await (await conn.execute("SELECT name FROM agent.schema_migrations")).fetchall()}
        for name, sql in files:
            if name not in done:
                await conn.execute(sql, prepare=False)  # several statements: never prepared
                await conn.execute("INSERT INTO agent.schema_migrations (name) VALUES (%s)", (name,))
                applied.append(name)
    return applied


@dataclass
class PostgresStorage:
    """The stores and checkpointer over one pool."""

    runs: PostgresRunStore
    chats: PostgresChatStore
    checkpointer: Any  # langgraph AsyncPostgresSaver
    pool: Any


@asynccontextmanager
async def open_postgres(dsn: str, *, min_size: int = 1, max_size: int = 10) -> AsyncIterator[PostgresStorage]:
    """Open the pool, migrate, set up the checkpointer, and close on exit."""
    try:
        from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver
        from psycopg.rows import dict_row
        from psycopg_pool import AsyncConnectionPool
    except ImportError as err:
        raise RuntimeError("install the 'postgres' extra for Postgres storage") from err
    kwargs = {"autocommit": True, "prepare_threshold": 0, "row_factory": dict_row, "options": "-c search_path=agent"}
    async with AsyncConnectionPool(dsn, min_size=min_size, max_size=max_size, kwargs=kwargs, open=False) as pool:
        saver = AsyncPostgresSaver(pool)
        async with pool.connection() as conn:
            await conn.execute("SELECT pg_advisory_lock(hashtext(%s))", (_LOCK_KEY,))
            try:
                await migrate(conn)
                await saver.setup()
            finally:
                await conn.execute("SELECT pg_advisory_unlock(hashtext(%s))", (_LOCK_KEY,))
        yield PostgresStorage(PostgresRunStore(pool), PostgresChatStore(pool), saver, pool)


class PostgresRunStore:
    """RunStore over ``agent.runs`` and ``agent.run_entries``."""

    def __init__(self, pool: Any) -> None:
        self._pool = pool

    async def create(self, run: RunInfo, at_ms: int) -> bool:
        """See RunStore.create. The partial unique index on chat_id refuses
        a second unfinished run."""
        async with self._pool.connection() as conn:
            cur = await conn.execute(
                "INSERT INTO agent.runs"
                " (run_id, chat_id, user_id, workspace_id, status, created_at_ms, running_since_ms)"
                " VALUES (%s, %s, %s, %s, 'running', %s, %s)"
                " ON CONFLICT (chat_id) WHERE status <> 'finished' DO NOTHING",
                (run.run_id, run.chat_id, run.user_id, run.workspace_id, at_ms, at_ms),
            )
            return cur.rowcount == 1

    async def append(self, run_id: str, entry: TraceEntry) -> None:
        """See RunStore.append. Only the run's owner appends, one at a time,
        so the next sequence number cannot race."""
        async with self._pool.connection() as conn:
            await conn.execute(
                "INSERT INTO agent.run_entries (run_id, seq, at_ms, entry)"
                " SELECT %s, coalesce(max(seq), 0) + 1, %s, %s FROM agent.run_entries WHERE run_id = %s",
                (run_id, entry.at_ms, _jsonb(item_to_json(entry.item)), run_id),
            )

    async def get(self, run_id: str) -> RunTrace | None:
        """See RunStore.get."""
        async with self._pool.connection() as conn, conn.transaction():
            row = await (await conn.execute("SELECT * FROM agent.runs WHERE run_id = %s", (run_id,))).fetchone()
            if row is None:
                return None
            entries = await (
                await conn.execute(
                    "SELECT at_ms, entry FROM agent.run_entries WHERE run_id = %s ORDER BY seq", (run_id,)
                )
            ).fetchall()
        status = RunStatus(row["status"])
        usage = None
        if status is RunStatus.FINISHED:
            usage = RunUsage(row["steps"], row["input_tokens"], row["output_tokens"], row["cost_usd"])
        return RunTrace(
            row["run_id"],
            row["chat_id"],
            row["user_id"],
            [TraceEntry(e["at_ms"], item_from_json(e["entry"])) for e in entries],
            status,
            usage,
        )

    async def pause(self, paused: PausedRun) -> bool:
        """See RunStore.pause."""
        async with self._pool.connection() as conn:
            cur = await conn.execute(
                "UPDATE agent.runs SET status = 'paused', paused_at_ms = %s, confirmation_id = %s, paused_state = %s"
                " WHERE run_id = %s AND status = 'running'",
                (paused.paused_at_ms, paused.confirmation_id, _jsonb(paused_to_json(paused)), paused.run.run_id),
            )
            return cur.rowcount == 1

    async def claim_paused(self, run_id: str, confirmation_id: str, at_ms: int) -> PausedRun | None:
        """See RunStore.claim_paused. UPDATE ... RETURNING returns the state
        as it was, so the claim and the read are one statement."""
        async with self._pool.connection() as conn:
            row = await (
                await conn.execute(
                    "UPDATE agent.runs AS r SET status = 'running', running_since_ms = %s,"
                    " paused_at_ms = NULL, confirmation_id = NULL, paused_state = NULL"
                    " FROM (SELECT run_id, paused_state FROM agent.runs WHERE run_id = %s FOR UPDATE) AS old"
                    " WHERE r.run_id = old.run_id AND r.status = 'paused' AND (%s = '' OR r.confirmation_id = %s)"
                    " RETURNING old.paused_state",
                    (at_ms, run_id, confirmation_id, confirmation_id),
                )
            ).fetchone()
        return None if row is None else paused_from_json(row["paused_state"])

    async def claim_stale(self, run_id: str, running_before_ms: int, at_ms: int) -> bool:
        """See RunStore.claim_stale."""
        async with self._pool.connection() as conn:
            cur = await conn.execute(
                "UPDATE agent.runs SET running_since_ms = %s"
                " WHERE run_id = %s AND status = 'running' AND running_since_ms < %s",
                (at_ms, run_id, running_before_ms),
            )
            return cur.rowcount == 1

    async def stale(self, *, paused_before_ms: int, running_before_ms: int) -> Sequence[str]:
        """See RunStore.stale."""
        async with self._pool.connection() as conn:
            rows = await (
                await conn.execute(
                    "SELECT run_id FROM agent.runs WHERE status = 'paused' AND paused_at_ms < %s"
                    " UNION ALL SELECT run_id FROM agent.runs WHERE status = 'running' AND running_since_ms < %s",
                    (paused_before_ms, running_before_ms),
                )
            ).fetchall()
        return [r["run_id"] for r in rows]

    async def finish(self, run_id: str, usage: RunUsage) -> None:
        """See RunStore.finish."""
        async with self._pool.connection() as conn:
            await conn.execute(
                "UPDATE agent.runs SET status = 'finished',"
                " finished_at_ms = (extract(epoch FROM now()) * 1000)::bigint,"
                " paused_at_ms = NULL, confirmation_id = NULL, paused_state = NULL,"
                " steps = %s, input_tokens = %s, output_tokens = %s, cost_usd = %s"
                " WHERE run_id = %s",
                (usage.steps, usage.input_tokens, usage.output_tokens, usage.cost_usd, run_id),
            )


class PostgresChatStore:
    """ChatStore over ``agent.chats`` and ``agent.chat_turns``."""

    def __init__(self, pool: Any) -> None:
        self._pool = pool

    async def create(self, chat: Chat) -> None:
        """See ChatStore.create."""
        async with self._pool.connection() as conn, conn.transaction():
            await conn.execute(
                "INSERT INTO agent.chats (chat_id, user_id, workspace_id, note_id, title, updated_at_ms)"
                " VALUES (%s, %s, %s, %s, %s, %s)",
                (chat.chat_id, chat.user_id, chat.workspace_id, chat.note_id, chat.title, chat.updated_at_ms),
            )
            await self._insert_turns(conn, chat.chat_id, 0, chat.turns)

    async def get(self, chat_id: str) -> Chat | None:
        """See ChatStore.get."""
        async with self._pool.connection() as conn, conn.transaction():
            row = await (await conn.execute("SELECT * FROM agent.chats WHERE chat_id = %s", (chat_id,))).fetchone()
            if row is None:
                return None
            turns = await (
                await conn.execute(
                    "SELECT role, text FROM agent.chat_turns WHERE chat_id = %s ORDER BY seq", (chat_id,)
                )
            ).fetchall()
        return _chat(row, [ChatTurn(t["role"], t["text"]) for t in turns])

    async def list(self, user_id: str, workspace_id: str, *, limit: int, after: str) -> Sequence[Chat]:
        """See ChatStore.list. Turns are not loaded: a listing needs titles."""
        query = "SELECT * FROM agent.chats WHERE user_id = %s AND workspace_id = %s"
        args: list[Any] = [user_id, workspace_id]
        if after:
            # The cursor chat's position in (updated_at_ms DESC, chat_id ASC);
            # a cursor that no longer matches gives an empty page, as in memory.
            query += (
                " AND EXISTS (SELECT 1 FROM agent.chats c WHERE c.chat_id = %s AND c.user_id = %s"
                " AND c.workspace_id = %s AND (agent.chats.updated_at_ms < c.updated_at_ms"
                " OR (agent.chats.updated_at_ms = c.updated_at_ms AND agent.chats.chat_id > c.chat_id)))"
            )
            args += [after, user_id, workspace_id]
        query += " ORDER BY updated_at_ms DESC, chat_id LIMIT %s"
        args.append(limit)
        async with self._pool.connection() as conn:
            rows = await (await conn.execute(query, args)).fetchall()
        return [_chat(r, []) for r in rows]

    async def append_turns(self, chat_id: str, turns: Sequence[ChatTurn], at_ms: int) -> None:
        """See ChatStore.append_turns."""
        async with self._pool.connection() as conn, conn.transaction():
            row = await (
                await conn.execute("SELECT title FROM agent.chats WHERE chat_id = %s FOR UPDATE", (chat_id,))
            ).fetchone()
            if row is None:
                return
            last = await (
                await conn.execute(
                    "SELECT coalesce(max(seq), 0) AS n FROM agent.chat_turns WHERE chat_id = %s", (chat_id,)
                )
            ).fetchone()
            await self._insert_turns(conn, chat_id, last["n"], turns)
            title = row["title"]
            if not title:
                first = await (
                    await conn.execute(
                        "SELECT role, text FROM agent.chat_turns"
                        " WHERE chat_id = %s AND role = 'user' ORDER BY seq LIMIT 1",
                        (chat_id,),
                    )
                ).fetchone()
                title = chat_title([ChatTurn(first["role"], first["text"])]) if first else ""
            await conn.execute(
                "UPDATE agent.chats SET updated_at_ms = %s, title = %s WHERE chat_id = %s", (at_ms, title, chat_id)
            )

    @staticmethod
    async def _insert_turns(conn: Any, chat_id: str, after_seq: int, turns: Sequence[ChatTurn]) -> None:
        for i, turn in enumerate(turns, after_seq + 1):
            await conn.execute(
                "INSERT INTO agent.chat_turns (chat_id, seq, role, text) VALUES (%s, %s, %s, %s)",
                (chat_id, i, turn.role, turn.text),
            )


def _chat(row: dict[str, Any], turns: list[ChatTurn]) -> Chat:
    return Chat(
        row["chat_id"], row["user_id"], row["workspace_id"], row["note_id"], row["title"], row["updated_at_ms"], turns
    )
