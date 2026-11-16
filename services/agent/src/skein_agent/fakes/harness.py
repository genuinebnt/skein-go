"""Wiring the agent entirely from fakes, for tests and the evals.

``main`` does the same wiring with real adapters (``adapters/wiring.py``).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from langchain_core.language_models import BaseChatModel
from langgraph.checkpoint.base import BaseCheckpointSaver
from langgraph.checkpoint.memory import InMemorySaver

from ..adapters.memory import InMemoryChatStore, InMemoryRunStore
from ..app.chats import Chats
from ..app.ports import ChatStore, ModelProvider, RateLimiter, RunStore
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
    run_store: RunStore
    chat_store: ChatStore
    model: BaseChatModel
    tokens: dict[str, str] = field(default_factory=dict)
    checkpointer: BaseCheckpointSaver[Any] | None = None
    rebuild: Any = None  # builds a fresh agent over the same stores; see restarted()

    def principal(self, user_id: str) -> Principal:
        """The authenticated caller for a seeded user."""
        return Principal(user_id, self.tokens[user_id])

    def restarted(self) -> Harness:
        """A new agent process over the same stores, checkpointer, workspace
        and model script: what a restart looks like to a paused run."""
        return self.rebuild()


def build_harness(
    workspace: FakeWorkspace,
    tokens: dict[str, str],
    *,
    script: list[Any] | None = None,
    provider: ModelProvider | None = None,
    settings: RunSettings | None = None,
    rate_limiter: RateLimiter | None = None,
    cache: InMemoryCache | None = None,
    run_store: RunStore | None = None,
    chat_store: ChatStore | None = None,
    checkpointer: BaseCheckpointSaver[Any] | None = None,
) -> Harness:
    """Wire the agent to ``workspace``. Give a ``script`` for the scripted
    model, or a ``provider`` (the evals' live mode passes Anthropic). The
    stores and checkpointer default to in-memory ones; pass real ones to run
    the same tests against Postgres."""
    if provider is None:
        provider = ScriptedModelProvider(ScriptedChatModel(script=list(script or [])))
    clock = SystemClock()
    cache = cache or InMemoryCache(clock)
    services = workspace.services()
    executor = ToolExecutor(build_catalogue(), cache)
    run_store = run_store if run_store is not None else InMemoryRunStore()
    chat_store = chat_store if chat_store is not None else InMemoryChatStore()
    checkpointer = checkpointer if checkpointer is not None else InMemorySaver()
    runs = AgentRuns(
        services=services,
        executor=executor,
        model_provider=provider,
        run_store=run_store,
        chat_store=chat_store,
        rate_limiter=rate_limiter or AllowAll(),
        clock=clock,
        checkpointer=checkpointer,
        settings=settings,
    )
    chats = Chats(chat_store, services.identity, clock)

    def rebuild() -> Harness:
        return build_harness(
            workspace,
            tokens,
            provider=provider,
            settings=settings,
            rate_limiter=rate_limiter,
            cache=cache,
            run_store=run_store,
            chat_store=chat_store,
            checkpointer=checkpointer,
        )

    model = provider.chat_model()
    return Harness(
        workspace, runs, chats, executor, cache, run_store, chat_store, model, dict(tokens), checkpointer, rebuild
    )
