"""Port interfaces the use cases depend on; adapters implement them.

Each is deliberately small (ADR-0012) so an in-memory fake is a few lines
and a second implementation (Postgres, Redis, Pub/Sub) can be swapped in by
`skein_ml.main` without touching the use cases.

Every method that can block on I/O is synchronous: the gRPC server runs
handlers on a thread pool, and the consumer runs on its own thread.
"""

from __future__ import annotations

from collections.abc import Callable, Iterable, Mapping, Sequence
from typing import Protocol

import numpy as np

from ..domain.embedding import Embedder
from ..domain.notes import Note
from .events import Event

__all__ = [
    "Cache",
    "Clock",
    "Embedder",
    "EmbeddingRepository",
    "EventHandler",
    "EventSource",
    "NoteRepository",
    "ProcessedEvents",
]


class NoteRepository(Protocol):
    """Notes as projected from ``documents.events``."""

    def get(self, note_id: str) -> Note | None:
        """The note, or None if ml has never heard of it (or it was purged)."""
        ...

    def put(self, note: Note) -> None:
        """Insert or replace."""
        ...

    def delete(self, note_id: str) -> None:
        """Remove; deleting an unknown id is not an error."""
        ...

    def in_workspace(self, workspace_id: str) -> list[Note]:
        """Every note of the workspace, trashed ones included, sorted by id."""
        ...


class EmbeddingRepository(Protocol):
    """Block embeddings, per note and embedding space."""

    def get(self, note_id: str, model_id: str) -> dict[str, np.ndarray]:
        """Block id to unit vector; empty when nothing is stored."""
        ...

    def put(self, note_id: str, model_id: str, vectors: Mapping[str, np.ndarray]) -> None:
        """Insert or replace the given blocks' vectors."""
        ...

    def delete_blocks(self, note_id: str, block_ids: Iterable[str]) -> None:
        """Remove some blocks' vectors in every embedding space."""
        ...

    def delete_note(self, note_id: str) -> None:
        """Remove every vector of the note."""
        ...


class ProcessedEvents(Protocol):
    """Event ids already applied, for idempotent consumption (ADR-0006).

    Callers check `contains`, apply, then `record`: a crash between apply and
    record means one harmless re-apply (every handler is an upsert), whereas
    recording first would lose the event.
    """

    def contains(self, event_id: str) -> bool:
        """Whether ``event_id`` was recorded."""
        ...

    def record(self, event_id: str) -> None:
        """Remember ``event_id`` as applied."""
        ...


class Cache(Protocol):
    """Byte-valued cache with TTLs and tag-based invalidation.

    Tags group keys so a change can drop every dependent entry at once
    (for example ``workspace:<id>`` on every search result of a workspace).
    A cache may lose entries at any time; callers treat a miss as normal.
    """

    def get(self, key: str) -> bytes | None:
        """The value, or None on a miss or after expiry."""
        ...

    def set(self, key: str, value: bytes, ttl_seconds: float, tags: Sequence[str] = ()) -> None:
        """Store ``value`` for ``ttl_seconds`` under ``key``, attached to ``tags``."""
        ...

    def invalidate_tags(self, tags: Sequence[str]) -> int:
        """Drop every key attached to any of ``tags``; returns how many were dropped."""
        ...


EventHandler = Callable[[Event], None]
"""Applies one event; raising means "not handled, redeliver"."""


class EventSource(Protocol):
    """A subscription to ``documents.events``."""

    def start(self, handler: EventHandler) -> None:
        """Begin delivering events to ``handler`` in the background."""
        ...

    def stop(self, timeout: float) -> None:
        """Stop delivering, waiting up to ``timeout`` seconds for in-flight events."""
        ...


Clock = Callable[[], float]
"""Monotonic seconds, injectable so TTL tests need no sleeping."""
