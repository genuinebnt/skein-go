"""``migrate`` against a recording connection: no database needed.

The service's role owns only its schema (deploy/compose/postgres/init.sql),
and PostgreSQL checks the database CREATE privilege for ``CREATE SCHEMA IF
NOT EXISTS`` before it checks existence, so migrating must not issue that
statement when the schema is already there.
"""

from __future__ import annotations

from contextlib import asynccontextmanager

from skein_agent.adapters.postgres import migrate


class _Cursor:
    def __init__(self, rows):
        self._rows = rows

    async def fetchone(self):
        return self._rows[0] if self._rows else None

    async def fetchall(self):
        return self._rows


class _RecordingConnection:
    """Records statements; knows only whether the schema exists."""

    def __init__(self, schema_exists: bool):
        self.schema_exists = schema_exists
        self.statements: list[str] = []

    @asynccontextmanager
    async def transaction(self):
        yield

    async def execute(self, sql, params=None, prepare=None):
        self.statements.append(sql)
        if "pg_namespace" in sql:
            return _Cursor([{"?column?": 1}] if self.schema_exists else [])
        if sql.startswith("CREATE SCHEMA"):
            if self.schema_exists:
                raise PermissionError("permission denied for database skein")
            self.schema_exists = True
        return _Cursor([])


async def test_migrate_does_not_create_an_existing_schema(tmp_path):
    (tmp_path / "0001_init.sql").write_text("SELECT 1;")
    conn = _RecordingConnection(schema_exists=True)
    assert await migrate(conn, tmp_path) == ["0001_init.sql"]
    assert not any(s.startswith("CREATE SCHEMA") for s in conn.statements)


async def test_migrate_creates_a_missing_schema_once(tmp_path):
    conn = _RecordingConnection(schema_exists=False)
    await migrate(conn, tmp_path)
    assert [s for s in conn.statements if s.startswith("CREATE SCHEMA")] == ["CREATE SCHEMA agent"]
