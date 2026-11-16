"""In-memory implementations of every port, for tests and single-process runs.

They are real implementations, not mocks: the service runs on them when no
Postgres or Redis is configured (the projection is then rebuilt from the
event stream on restart). Each is thread-safe with one lock.

`InMemoryCache` takes an injectable clock so TTL behaviour is tested
without sleeping. `InMemoryEventSource` delivers published events on a
worker thread with at-least-once semantics: a handler that raises leaves the
event queued for redelivery, as Pub/Sub would after a nack.
"""

from __future__ import annotations

import copy
import queue
import threading
import time
from collections import OrderedDict
from collections.abc import Callable, Iterable, Mapping, Sequence

import numpy as np

from ..app.events import Event
from ..app.ports import Clock, EventHandler
from ..domain.notes import Note

__all__ = [
    "InMemoryCache",
    "InMemoryEmbeddingRepository",
    "InMemoryEventSource",
    "InMemoryNoteRepository",
    "InMemoryProcessedEvents",
]


class InMemoryNoteRepository:
    """`NoteRepository` over a dict; returns copies so callers cannot alias state."""

    def __init__(self) -> None:
        self._notes: dict[str, Note] = {}
        self._lock = threading.Lock()

    def get(self, note_id: str) -> Note | None:
        """See `NoteRepository.get`."""
        with self._lock:
            note = self._notes.get(note_id)
            return copy.deepcopy(note) if note is not None else None

    def put(self, note: Note) -> None:
        """See `NoteRepository.put`."""
        with self._lock:
            self._notes[note.note_id] = copy.deepcopy(note)

    def delete(self, note_id: str) -> None:
        """See `NoteRepository.delete`."""
        with self._lock:
            self._notes.pop(note_id, None)

    def in_workspace(self, workspace_id: str) -> list[Note]:
        """See `NoteRepository.in_workspace`."""
        with self._lock:
            return [copy.deepcopy(self._notes[i]) for i in sorted(self._notes)
                    if self._notes[i].workspace_id == workspace_id]


class InMemoryEmbeddingRepository:
    """`EmbeddingRepository` keyed by (note, model)."""

    def __init__(self) -> None:
        self._vectors: dict[tuple[str, str], dict[str, np.ndarray]] = {}
        self._lock = threading.Lock()

    def get(self, note_id: str, model_id: str) -> dict[str, np.ndarray]:
        """See `EmbeddingRepository.get`."""
        with self._lock:
            return {b: v.copy() for b, v in self._vectors.get((note_id, model_id), {}).items()}

    def put(self, note_id: str, model_id: str, vectors: Mapping[str, np.ndarray]) -> None:
        """See `EmbeddingRepository.put`."""
        with self._lock:
            stored = self._vectors.setdefault((note_id, model_id), {})
            for block_id, vector in vectors.items():
                stored[block_id] = np.asarray(vector, dtype=np.float32).copy()

    def delete_blocks(self, note_id: str, block_ids: Iterable[str]) -> None:
        """See `EmbeddingRepository.delete_blocks`."""
        block_ids = list(block_ids)
        with self._lock:
            for (note, _), stored in self._vectors.items():
                if note == note_id:
                    for block_id in block_ids:
                        stored.pop(block_id, None)

    def delete_note(self, note_id: str) -> None:
        """See `EmbeddingRepository.delete_note`."""
        with self._lock:
            for key in [k for k in self._vectors if k[0] == note_id]:
                del self._vectors[key]


class InMemoryProcessedEvents:
    """`ProcessedEvents` remembering the most recent ``capacity`` ids.

    Bounded because the stream is unbounded; a redelivery older than the
    window would be applied twice, which every handler tolerates (they are
    upserts), so the window only saves work.
    """

    def __init__(self, capacity: int = 100_000) -> None:
        self._seen: OrderedDict[str, None] = OrderedDict()
        self._capacity = capacity
        self._lock = threading.Lock()

    def contains(self, event_id: str) -> bool:
        """See `ProcessedEvents.contains`."""
        with self._lock:
            return event_id in self._seen

    def record(self, event_id: str) -> None:
        """See `ProcessedEvents.record`."""
        with self._lock:
            self._seen[event_id] = None
            self._seen.move_to_end(event_id)
            if len(self._seen) > self._capacity:
                self._seen.popitem(last=False)


class InMemoryCache:
    """`Cache` with TTLs and tags, bounded by ``max_entries`` (oldest evicted)."""

    def __init__(self, clock: Clock = time.monotonic, max_entries: int = 50_000) -> None:
        self._clock = clock
        self._max_entries = max_entries
        self._entries: OrderedDict[str, tuple[bytes, float, tuple[str, ...]]] = OrderedDict()
        self._tagged: dict[str, set[str]] = {}
        self._lock = threading.Lock()

    def get(self, key: str) -> bytes | None:
        """See `Cache.get`."""
        with self._lock:
            entry = self._entries.get(key)
            if entry is None:
                return None
            value, expires, _ = entry
            if self._clock() >= expires:
                self._drop(key)
                return None
            return value

    def set(self, key: str, value: bytes, ttl_seconds: float, tags: Sequence[str] = ()) -> None:
        """See `Cache.set`."""
        with self._lock:
            self._drop(key)
            self._entries[key] = (bytes(value), self._clock() + ttl_seconds, tuple(tags))
            for tag in tags:
                self._tagged.setdefault(tag, set()).add(key)
            while len(self._entries) > self._max_entries:
                self._drop(next(iter(self._entries)))

    def invalidate_tags(self, tags: Sequence[str]) -> int:
        """See `Cache.invalidate_tags`."""
        with self._lock:
            keys = set().union(*(self._tagged.get(tag, set()) for tag in tags)) if tags else set()
            for key in keys:
                self._drop(key)
            return len(keys)

    def __len__(self) -> int:
        return len(self._entries)

    def _drop(self, key: str) -> None:
        entry = self._entries.pop(key, None)
        if entry is None:
            return
        for tag in entry[2]:
            members = self._tagged.get(tag)
            if members is not None:
                members.discard(key)
                if not members:
                    del self._tagged[tag]


class InMemoryEventSource:
    """`EventSource` fake: ``publish`` enqueues, a worker thread delivers.

    ``decode`` turns published items into `Event`s, so tests can publish
    Protobuf bytes and exercise the real codec, or `Event` objects directly.
    """

    def __init__(self, decode: Callable[[object], Event] | None = None,
                 retry_delay: float = 0.01) -> None:
        self._queue: queue.Queue[object] = queue.Queue()
        self._decode = decode or (lambda item: item)  # type: ignore[assignment,return-value]
        self._retry_delay = retry_delay
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._idle = threading.Condition()
        self._in_flight = 0
        self.delivered = 0
        self.failures = 0

    def publish(self, item: object) -> None:
        """Enqueue one event (or encoded event) for delivery."""
        with self._idle:
            self._in_flight += 1
        self._queue.put(item)

    def start(self, handler: EventHandler) -> None:
        """See `EventSource.start`."""
        self._stop.clear()
        self._thread = threading.Thread(target=self._run, args=(handler,), daemon=True,
                                        name="documents-events")
        self._thread.start()

    def stop(self, timeout: float) -> None:
        """See `EventSource.stop`; undelivered events stay queued."""
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout)

    def wait_idle(self, timeout: float = 5.0) -> bool:
        """Block until every published event has been handled successfully."""
        with self._idle:
            return self._idle.wait_for(lambda: self._in_flight == 0, timeout)

    def _run(self, handler: EventHandler) -> None:
        while not self._stop.is_set():
            try:
                item = self._queue.get(timeout=0.05)
            except queue.Empty:
                continue
            try:
                handler(self._decode(item))
            except Exception:
                self.failures += 1
                self._queue.put(item)  # nack: redeliver later
                self._stop.wait(self._retry_delay)
                continue
            self.delivered += 1
            with self._idle:
                self._in_flight -= 1
                self._idle.notify_all()
