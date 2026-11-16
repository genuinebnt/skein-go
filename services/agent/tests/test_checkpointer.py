"""Checkpointer selection; the Postgres saver needs Docker and is skipped by
default (``pytest -m integration``)."""

from __future__ import annotations

import pytest
from langgraph.checkpoint.memory import InMemorySaver

from skein_agent.adapters.checkpointer import open_checkpointer


async def test_memory_checkpointer_is_the_default() -> None:
    async with open_checkpointer("memory") as saver:
        assert isinstance(saver, InMemorySaver)


async def test_unknown_or_unconfigured_checkpointers_are_rejected() -> None:
    with pytest.raises(ValueError):
        async with open_checkpointer("sqlite"):
            pass
    with pytest.raises(ValueError, match="DSN"):
        async with open_checkpointer("postgres"):
            pass


@pytest.mark.integration
async def test_paused_run_state_survives_in_postgres(make_harness) -> None:
    testcontainers = pytest.importorskip("testcontainers.postgres")
    from langchain_core.messages import HumanMessage

    from skein_agent.app.graph import build_graph

    with testcontainers.PostgresContainer("postgres:17") as pg:
        dsn = pg.get_connection_url().replace("postgresql+psycopg2://", "postgresql://")
        async with open_checkpointer("postgres", dsn) as saver:
            h = make_harness([])
            graph = build_graph(h.executor, saver)
            config = {"configurable": {"thread_id": "t-1"}}
            await graph.aupdate_state(config, {"messages": [HumanMessage("hi")]})
            state = await graph.aget_state(config)
            assert state.values["messages"][0].content == "hi"
