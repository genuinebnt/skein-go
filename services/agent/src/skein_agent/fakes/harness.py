"""Wiring the agent entirely from fakes, for tests and the evals.

``main`` does the same wiring with real adapters (``adapters/wiring.py``).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from langchain_core.language_models import BaseChatModel
from langgraph.checkpoint.memory import InMemorySaver

from ..adapters.memory import InMemoryChatStore, InMemoryRunStore
from ..app.chats import Chats
from ..app.ports import ModelProvider, RateLimiter
from ..app.runs import AgentRuns, Principal, RunSettings
from ..app.tools.catalogue import build_catalogue
from ..app.tools.executor import ToolExecutor
from .infra import AllowAll, InMemoryCache, SystemClock
from .model import ScriptedChatModel, ScriptedModelProvider
from .workspace import FakeWorkspace


@dataclass
class Harness:
    """An agent wired to fakes, plus handles on every fake."""

    workspace: FakeWorkspace
    runs: AgentRuns
    chats: Chats
    executor: ToolExecutor
    cache: InMemoryCache
    run_store: InMemoryRunStore
    chat_store: InMemoryChatStore
    model: BaseChatModel
    tokens: dict[str, str] = field(default_factory=dict)

    def principal(self, user_id: str) -> Principal:
        """The authenticated caller for a seeded user."""
        return Principal(user_id, self.tokens[user_id])


def build_harness(
    workspace: FakeWorkspace,
    tokens: dict[str, str],
    *,
    script: list[Any] | None = None,
    provider: ModelProvider | None = None,
    settings: RunSettings | None = None,
    rate_limiter: RateLimiter | None = None,
    cache: InMemoryCache | None = None,
) -> Harness:
    """Wire the agent to ``workspace``. Give a ``script`` for the scripted
    model, or a ``provider`` (the evals' live mode passes Anthropic)."""
    if provider is None:
        provider = ScriptedModelProvider(ScriptedChatModel(script=list(script or [])))
    clock = SystemClock()
    cache = cache or InMemoryCache(clock)
    services = workspace.services()
    executor = ToolExecutor(build_catalogue(), cache)
    run_store, chat_store = InMemoryRunStore(), InMemoryChatStore()
    runs = AgentRuns(
        services=services,
        executor=executor,
        model_provider=provider,
        run_store=run_store,
        chat_store=chat_store,
        rate_limiter=rate_limiter or AllowAll(),
        clock=clock,
        checkpointer=InMemorySaver(),
        settings=settings,
    )
    chats = Chats(chat_store, services.identity, clock)
    return Harness(workspace, runs, chats, executor, cache, run_store, chat_store, provider.chat_model(), dict(tokens))
