"""Postgres implementations of the repository ports (optional ``postgres`` extra).

Tables live in the ``ml`` schema (``migrations/0001_init.sql``); `migrate`
applies pending files in name order and records them in
``ml.schema_migrations``. Each method is one short transaction on a shared
connection guarded by a lock: simple and correct, and the service's write
rate (one transaction per event) is far below what one connection handles.
A pool (``psycopg_pool``) is the upgrade if reads become hot.

Vectors are stored as little-endian float32 bytes with their dimension.
Integration tests (``-m integration``) need ``SKEIN_ML_TEST_DATABASE_URL``.
"""

from __future__ import annotations

import os
import threading
from collections.abc import Iterable, Mapping
from pathlib import Path

import numpy as np

from ..domain.notes import Note

__all__ = ["PostgresStore", "migrate"]

# The source tree's migrations/; installed images point SKEIN_ML_MIGRATIONS_DIR at a copy.
MIGRATIONS = Path(
    os.environ.get("SKEIN_ML_MIGRATIONS_DIR") or Path(__file__).resolve().parents[3] / "migrations"
)


def _connect(url: str):
    try:
        import psycopg
    except ImportError as error:  # pragma: no cover - depends on the environment
        raise RuntimeError(
            "install the 'postgres' extra: pip install 'skein-ml[postgres]'"
        ) from error
    return psycopg.connect(url, autocommit=False)


def migrate(connection, directory: Path = MIGRATIONS) -> list[str]:
    """Apply pending ``*.sql`` migrations in order; returns the names applied."""
    applied = []
    with connection.transaction():
        connection.execute("CREATE SCHEMA IF NOT EXISTS ml")
        connection.execute(
            "CREATE TABLE IF NOT EXISTS ml.schema_migrations "
            "(name text PRIMARY KEY, applied_at timestamptz DEFAULT now())"
        )
        done = {row[0] for row in connection.execute("SELECT name FROM ml.schema_migrations")}
        for path in sorted(directory.glob("*.sql")):
            if path.name in done:
                continue
            connection.execute(path.read_text())
            connection.execute("INSERT INTO ml.schema_migrations (name) VALUES (%s)", (path.name,))
            applied.append(path.name)
    return applied


class PostgresStore:
    """`NoteRepository`, `EmbeddingRepository` and `ProcessedEvents` on one connection.

    Use `notes`, `embeddings` and `processed`, which expose each port's
    method names without colliding.
    """

    def __init__(self, url: str, run_migrations: bool = True):
        self._connection = _connect(url)
        self._lock = threading.Lock()
        if run_migrations:
            with self._lock:
                migrate(self._connection)
        self.notes = _Notes(self)
        self.embeddings = _Embeddings(self)
        self.processed = _Processed(self)

    def close(self) -> None:
        """Close the connection."""
        self._connection.close()

    def _run(self, work):
        with self._lock, self._connection.transaction():
            return work(self._connection)


class _Notes:
    def __init__(self, store: PostgresStore):
        self._store = store

    def get(self, note_id: str) -> Note | None:
        def work(c):
            row = c.execute(
                "SELECT workspace_id, title, trashed, links FROM ml.notes WHERE note_id = %s",
                (note_id,),
            ).fetchone()
            if row is None:
                return None
            blocks = dict(
                c.execute(
                    "SELECT block_id, body FROM ml.blocks WHERE note_id = %s", (note_id,)
                ).fetchall()
            )
            return Note(note_id, row[0], row[1], row[2], blocks, tuple(row[3]))

        return self._store._run(work)

    def put(self, note: Note) -> None:
        def work(c):
            c.execute(
                "INSERT INTO ml.notes (note_id, workspace_id, title, trashed, links) "
                "VALUES (%s, %s, %s, %s, %s) ON CONFLICT (note_id) DO UPDATE SET "
                "workspace_id = EXCLUDED.workspace_id, title = EXCLUDED.title, "
                "trashed = EXCLUDED.trashed, links = EXCLUDED.links, updated_at = now()",
                (note.note_id, note.workspace_id, note.title, note.trashed, list(note.links)),
            )
            c.execute("DELETE FROM ml.blocks WHERE note_id = %s", (note.note_id,))
            with c.cursor() as cursor:
                cursor.executemany(
                    "INSERT INTO ml.blocks (note_id, block_id, body) VALUES (%s, %s, %s)",
                    [(note.note_id, b, t) for b, t in sorted(note.blocks.items())],
                )

        self._store._run(work)

    def delete(self, note_id: str) -> None:
        self._store._run(lambda c: c.execute("DELETE FROM ml.notes WHERE note_id = %s", (note_id,)))

    def in_workspace(self, workspace_id: str) -> list[Note]:
        def work(c):
            rows = c.execute(
                "SELECT note_id, title, trashed, links FROM ml.notes "
                "WHERE workspace_id = %s ORDER BY note_id",
                (workspace_id,),
            ).fetchall()
            blocks: dict[str, dict[str, str]] = {}
            for note_id, block_id, body in c.execute(
                "SELECT b.note_id, b.block_id, b.body FROM ml.blocks b JOIN ml.notes n "
                "USING (note_id) WHERE n.workspace_id = %s",
                (workspace_id,),
            ):
                blocks.setdefault(note_id, {})[block_id] = body
            return [
                Note(r[0], workspace_id, r[1], r[2], blocks.get(r[0], {}), tuple(r[3]))
                for r in rows
            ]

        return self._store._run(work)


class _Embeddings:
    def __init__(self, store: PostgresStore):
        self._store = store

    def get(self, note_id: str, model_id: str) -> dict[str, np.ndarray]:
        rows = self._store._run(
            lambda c: c.execute(
                "SELECT block_id, vector FROM ml.block_embeddings "
                "WHERE note_id = %s AND model_id = %s",
                (note_id, model_id),
            ).fetchall()
        )
        return {block: np.frombuffer(bytes(data), dtype="<f4").copy() for block, data in rows}

    def put(self, note_id: str, model_id: str, vectors: Mapping[str, np.ndarray]) -> None:
        rows = [
            (note_id, model_id, block, len(v), np.asarray(v, dtype="<f4").tobytes())
            for block, v in sorted(vectors.items())
        ]
        if not rows:
            return

        def work(c):
            with c.cursor() as cursor:
                cursor.executemany(
                    "INSERT INTO ml.block_embeddings (note_id, model_id, block_id, dimension, "
                    "vector) VALUES (%s, %s, %s, %s, %s) ON CONFLICT (note_id, model_id, block_id) "
                    "DO UPDATE SET dimension = EXCLUDED.dimension, vector = EXCLUDED.vector",
                    rows,
                )

        self._store._run(work)

    def delete_blocks(self, note_id: str, block_ids: Iterable[str]) -> None:
        ids = list(block_ids)
        if ids:
            self._store._run(
                lambda c: c.execute(
                    "DELETE FROM ml.block_embeddings WHERE note_id = %s AND block_id = ANY(%s)",
                    (note_id, ids),
                )
            )

    def delete_note(self, note_id: str) -> None:
        self._store._run(
            lambda c: c.execute("DELETE FROM ml.block_embeddings WHERE note_id = %s", (note_id,))
        )


class _Processed:
    def __init__(self, store: PostgresStore):
        self._store = store

    def contains(self, event_id: str) -> bool:
        return (
            self._store._run(
                lambda c: c.execute(
                    "SELECT 1 FROM ml.processed_events WHERE event_id = %s", (event_id,)
                ).fetchone()
            )
            is not None
        )

    def record(self, event_id: str) -> None:
        self._store._run(
            lambda c: c.execute(
                "INSERT INTO ml.processed_events (event_id) VALUES (%s) ON CONFLICT DO NOTHING",
                (event_id,),
            )
        )
