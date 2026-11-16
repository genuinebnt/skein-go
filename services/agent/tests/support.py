"""Helpers shared by the tests."""

from __future__ import annotations

import asyncio
from collections.abc import Callable
from typing import Any

from skein_agent.app.runs import ActiveRun, RunSettings
from skein_agent.domain.events import ConfirmationRequested, RunEvent, RunFinished
from skein_agent.evals import fixture
from skein_agent.fakes.harness import Harness

WORKSPACE = fixture.WORKSPACE
NOTES = fixture.NOTES


async def start(h: Harness, user: str, text: str) -> ActiveRun:
    """Create a chat for ``user`` and send one message."""
    principal = h.principal(user)
    chat_id = await h.chats.create(principal, WORKSPACE)
    return await h.runs.send_message(principal, chat_id, text)


async def collect(
    h: Harness,
    run: ActiveRun,
    *,
    answer: Callable[[ConfirmationRequested], bool | None] | None = None,
    timeout_s: float = 10.0,
) -> list[RunEvent]:
    """Stream a run to the end, answering confirmations with ``answer``
    (True approve, False decline, None leave pending)."""
    events: list[RunEvent] = []

    async def consume() -> None:
        async for event in run.stream():
            events.append(event)
            if isinstance(event, ConfirmationRequested) and answer is not None:
                decision = answer(event)
                if decision is not None:
                    await h.runs.confirm(h.principal(run.user_id), run.run_id, event.confirmation_id, decision)

    await asyncio.wait_for(consume(), timeout_s)
    return events


def finished(events: list[RunEvent]) -> RunFinished:
    """The last event, which must be RunFinished."""
    last = events[-1]
    assert isinstance(last, RunFinished), f"stream ended with {last!r}"
    assert sum(isinstance(e, RunFinished) for e in events) == 1, "exactly one RunFinished"
    return last


def settings(**budget: Any) -> RunSettings:
    """RunSettings with a custom budget."""
    from skein_agent.domain.budget import Budget

    extra = {k: budget.pop(k) for k in list(budget) if k in ("confirmation_timeout_s", "call_timeout_s")}
    return RunSettings(budget=Budget(**budget), **extra)
