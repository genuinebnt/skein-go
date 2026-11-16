"""Port interfaces the agent's use cases need. Each has a real adapter
(``adapters/``) and an in-memory fake (``fakes/``).

Downstream services are reached through one deliberately small interface,
``ServiceClient``: invoke an RPC by name with a protobuf request and the
caller's token. That is what lets a new RPC become a new tool with one entry
in the catalogue and no new client code (ADR-0009). The real adapter is a
generic wrapper over a generated gRPC stub; each fake is a small in-memory
servicer.
"""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass, field
from typing import Protocol

from google.protobuf.message import Message
from langchain_core.language_models import BaseChatModel

from ..domain.events import RunTrace, TraceEntry
from ..domain.ratelimit import Decision


class ServiceClient(Protocol):
    """One downstream gRPC service."""

    async def invoke(self, rpc: str, request: Message, *, token: str, timeout_s: float) -> Message:
        """Call ``rpc`` (the method name, for example ``"GetNote"``).

        ``token`` is sent as the bearer credential. Raises ServiceError for
        any non-OK status.
        """
        ...


class OpLogReader(Protocol):
    """Reads a note's CRDT operations, needed to write suggestions."""

    async def fetch_ops(
        self, note_id: str, until: dict[int, int], *, replica_id: int, token: str, timeout_s: float
    ) -> list[bytes]:
        """Return encoded batches, in causal order, covering at least ``until``
        (a version vector from GetNote). ``replica_id`` is the caller's
        registered replica, which the sync handshake names."""
        ...


@dataclass(frozen=True, slots=True)
class Services:
    """The services the agent's tools call."""

    documents: ServiceClient
    search: ServiceClient
    graph: ServiceClient
    ml: ServiceClient
    identity: ServiceClient
    notifications: ServiceClient
    op_log: OpLogReader

    def by_name(self, name: str) -> ServiceClient:
        """Look up a client by catalogue name ("documents", "search"...)."""
        client = getattr(self, name, None)
        if client is None or name == "op_log":
            raise KeyError(f"no service named {name!r}")
        return client


class ModelProvider(Protocol):
    """Creates the chat model for a run."""

    @property
    def model_name(self) -> str:
        """Model identifier, used for pricing and traces."""
        ...

    def chat_model(self) -> BaseChatModel:
        """A model instance; the graph binds tools to it."""
        ...


class RunStore(Protocol):
    """Run traces. In memory for now; a Postgres adapter follows."""

    async def create(self, run_id: str, chat_id: str, user_id: str) -> None:
        """Start an empty trace."""
        ...

    async def append(self, run_id: str, entry: TraceEntry) -> None:
        """Add one entry, in order."""
        ...

    async def get(self, run_id: str) -> RunTrace | None:
        """The full trace, or None."""
        ...


@dataclass(frozen=True, slots=True)
class ChatTurn:
    """One exchange in a chat's transcript."""

    role: str  # "user" or "assistant"
    text: str


@dataclass(slots=True)
class Chat:
    """A conversation in one workspace."""

    chat_id: str
    user_id: str
    workspace_id: str
    note_id: str
    title: str
    updated_at_ms: int
    turns: list[ChatTurn] = field(default_factory=list)


class ChatStore(Protocol):
    """Chats and their transcripts."""

    async def create(self, chat: Chat) -> None:
        """Store a new chat."""
        ...

    async def get(self, chat_id: str) -> Chat | None:
        """A chat by ID."""
        ...

    async def list(self, user_id: str, workspace_id: str, *, limit: int, after: str) -> Sequence[Chat]:
        """A user's chats in a workspace, most recently updated first,
        starting after the chat ID ``after`` (empty for the first page)."""
        ...

    async def append_turns(self, chat_id: str, turns: Sequence[ChatTurn], at_ms: int) -> None:
        """Add turns and bump updated_at."""
        ...


class Cache(Protocol):
    """A short-lived key-value cache (Redis in production)."""

    async def get(self, key: str) -> str | None:
        """The value, or None if absent or expired."""
        ...

    async def set(self, key: str, value: str, ttl_s: float) -> None:
        """Store with a time to live."""
        ...

    async def incr(self, key: str) -> int:
        """Atomically increment an integer (0 if absent) and return it."""
        ...


class RateLimiter(Protocol):
    """Per-key admission control for agent runs."""

    async def check(self, key: str) -> Decision:
        """Consume one unit for ``key`` if allowed."""
        ...


class Clock(Protocol):
    """Time source, injectable for tests."""

    def now(self) -> float:
        """Seconds since the Unix epoch."""
        ...

    def monotonic(self) -> float:
        """Seconds on a monotonic clock."""
        ...
