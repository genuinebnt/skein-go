"""Shared fixtures. Nothing here opens a network connection: every port is
an in-memory fake, the model is scripted, and OpenTelemetry has no SDK
installed (its API is a no-op)."""

from __future__ import annotations

import os
from collections.abc import Callable
from typing import Any

import pytest

os.environ.setdefault("OTEL_SDK_DISABLED", "true")

from skein_agent.evals import fixture
from skein_agent.fakes.harness import Harness, build_harness


@pytest.fixture
def fx() -> fixture.Fixture:
    """A fresh copy of the eval workspace."""
    return fixture.seed()


@pytest.fixture
def make_harness(fx: fixture.Fixture) -> Callable[..., Harness]:
    """Build a harness on the fixture workspace with a script."""

    def make(script: list[Any], **kwargs: Any) -> Harness:
        return build_harness(fx.workspace, fx.tokens, script=script, **kwargs)

    return make


async def _postgres_storage():
    """Postgres storage on SKEIN_AGENT_TEST_DATABASE_URL, or on a
    Testcontainers Postgres when it is unset (needs Docker)."""
    from contextlib import ExitStack

    from skein_agent.adapters.postgres import open_postgres
    from skein_agent.adapters.storage import Storage

    with ExitStack() as stack:
        dsn = os.environ.get("SKEIN_AGENT_TEST_DATABASE_URL", "")
        if not dsn:
            containers = pytest.importorskip("testcontainers.postgres", reason="needs Docker and testcontainers")
            pg = stack.enter_context(containers.PostgresContainer("postgres:17"))
            dsn = pg.get_connection_url().replace("postgresql+psycopg2://", "postgresql://")
        async with open_postgres(dsn, max_size=4) as storage:
            async with storage.pool.connection() as conn:
                tables = await (
                    await conn.execute("SELECT tablename FROM pg_tables WHERE schemaname = 'agent'")
                ).fetchall()
                names = ", ".join(f"agent.{t['tablename']}" for t in tables if t["tablename"] != "schema_migrations")
                await conn.execute(f"TRUNCATE {names} CASCADE")
            yield Storage(storage.runs, storage.chats, storage.checkpointer, durable=True)


@pytest.fixture(params=["memory", pytest.param("postgres", marks=pytest.mark.integration)])
async def storage(request):
    """Chats, runs and checkpointer: in memory, and (``-m integration``)
    Postgres. Tests taking this fixture state the stores' contract once for
    both."""
    if request.param == "memory":
        from skein_agent.adapters.storage import open_storage

        async with open_storage("memory") as s:
            yield s
        return
    async for s in _postgres_storage():
        yield s
