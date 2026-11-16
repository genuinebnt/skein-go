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

from ..domain.budget import RunUsage
from ..domain.events import RunTrace, TraceEntry
from ..domain.ratelimit import Decision
from ..domain.untrusted import Source


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


@dataclass(frozen=True, slots=True)
class RunInfo:
    """What a run is, fixed when it starts."""

    run_id: str
    chat_id: str
    user_id: str
    workspace_id: str


@dataclass(frozen=True, slots=True)
class PausedRun:
    """Everything a paused run needs to resume in another process, apart
    from the graph state, which the LangGraph checkpointer holds under the
    thread ID ``run_id``.

    No token is stored: resuming asks identity for a new delegated token with
    the token of the user who answers the confirmation. ``nonce`` and
    ``sources`` keep the untrusted-data delimiters and the citation labels
    the model has already seen valid; ``usage`` and ``active_seconds`` keep
    the budget from starting over.
    """

    run: RunInfo
    user_text: str
    input_count: int  # messages given to the graph; later ones are the run's own
    confirmation_id: str
    call_id: str
    tool: str
    nonce: str
    sources: tuple[Source, ...]
    replica_id: int
    usage: RunUsage
    active_seconds: float
    paused_at_ms: int


class RunStore(Protocol):
    """Runs: their status, traces and paused state.

    Status changes are compare-and-set, so several processes sharing a store
    agree on who owns a run: ``create`` refuses a second unfinished run in a
    chat, and each way out of a pause (``claim_paused``, ``claim_stale``)
    succeeds for exactly one caller. Only the owner of a running run calls
    ``pause`` and ``finish``.
    """

    async def create(self, run: RunInfo, at_ms: int) -> bool:
        """Start a running run with an empty trace. False, and nothing
        stored, if the chat already has an unfinished run."""
        ...

    async def append(self, run_id: str, entry: TraceEntry) -> None:
        """Add one trace entry, in order."""
        ...

    async def get(self, run_id: str) -> RunTrace | None:
        """The run's trace and status, or None."""
        ...

    async def pause(self, paused: PausedRun) -> bool:
        """Move a running run to paused and keep ``paused``. False if the run
        is not running (a sweeper has already ended it)."""
        ...

    async def claim_paused(self, run_id: str, confirmation_id: str, at_ms: int) -> PausedRun | None:
        """Move a paused run back to running and return its state, if it is
        paused on ``confirmation_id`` (any confirmation when empty). None if
        it is not, including when another caller claimed it first."""
        ...

    async def claim_stale(self, run_id: str, running_before_ms: int, at_ms: int) -> bool:
        """Claim a run left running since before ``running_before_ms`` by a
        process that stopped, so the caller can finish it. Restarts its
        running clock at ``at_ms`` so no one else claims it too."""
        ...

    async def stale(self, *, paused_before_ms: int, running_before_ms: int) -> Sequence[str]:
        """IDs of runs paused or running since before the given times: the
        candidates for ``claim_paused`` and ``claim_stale``."""
        ...

    async def finish(self, run_id: str, usage: RunUsage) -> None:
        """Mark the run finished with its final usage."""
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


TITLE_CHARS = 60


def chat_title(turns: Sequence[ChatTurn]) -> str:
    """A chat's title: the start of its first user message."""
    first = next((t.text for t in turns if t.role == "user"), "")
    return first if len(first) <= TITLE_CHARS else first[: TITLE_CHARS - 1] + "…"


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
        """Add turns, bump updated_at, and set the title (``chat_title``) if
        it is empty."""
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
