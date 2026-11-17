"""``migrate`` against a recording connection: no database needed.

The service's role owns only its schema (deploy/compose/postgres/init.sql),
and PostgreSQL checks the database CREATE privilege for ``CREATE SCHEMA IF
NOT EXISTS`` before it checks existence, so migrating must not issue that
statement when the schema is already there.
"""

from __future__ import annotations

from contextlib import contextmanager

from skein_ml.adapters.postgres import migrate


class _Cursor(list):
    def fetchone(self):
        return self[0] if self else None


class _RecordingConnection:
    """Records statements; knows only whether the schema exists."""

    def __init__(self, schema_exists: bool):
        self.schema_exists = schema_exists
        self.statements: list[str] = []

    @contextmanager
    def transaction(self):
        yield

    def execute(self, sql, params=None):
        self.statements.append(sql)
        if "pg_namespace" in sql:
            return _Cursor([(1,)] if self.schema_exists else [])
        if sql.startswith("CREATE SCHEMA"):
            if self.schema_exists:
                raise PermissionError("permission denied for database skein")
            self.schema_exists = True
        return _Cursor([])


def test_migrate_does_not_create_an_existing_schema(tmp_path):
    (tmp_path / "0001_init.sql").write_text("SELECT 1;")
    connection = _RecordingConnection(schema_exists=True)
    assert migrate(connection, tmp_path) == ["0001_init.sql"]
    assert not any(s.startswith("CREATE SCHEMA") for s in connection.statements)


def test_migrate_creates_a_missing_schema_once(tmp_path):
    connection = _RecordingConnection(schema_exists=False)
    migrate(connection, tmp_path)
    created = [s for s in connection.statements if s.startswith("CREATE SCHEMA")]
    assert created == ["CREATE SCHEMA ml"]
