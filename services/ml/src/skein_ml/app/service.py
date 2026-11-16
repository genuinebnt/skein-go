"""The ml use cases: index events, semantic search, discovery, clusters, benchmarks.

`MLApp` depends only on the ports in `skein_ml.app.ports`, so tests build it
with in-memory fakes and `skein_ml.main` with Postgres, Redis and Pub/Sub.

Consistency model: the repositories are the source of truth; the vector
indexes are a per-process cache of them, loaded per workspace on first use
and then kept current by the events this process applies. A workspace is
dropped from memory (and reloaded lazily) whenever an event would move
blocks between workspaces, which keeps that rare path simple.

Concurrency: one re-entrant lock serialises index mutation and search. The
gRPC pool and the consumer thread both go through it. That is the simplest
correct choice at this scale; per-workspace locks are the next step.

Caching (`Cache` port):

- query embeddings under ``embedding:<model>:<sha256(text)>``, tagged
  ``model:<model>``. Content-addressed, so they never go stale.
- block embeddings the same way, so re-sending unchanged text is free;
- search results and clusters, tagged ``workspace:<id>``, invalidated by
  every event that changes the workspace.

Note contents are never logged.
"""

from __future__ import annotations

import hashlib
import json
import logging
import math
import threading
import time
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field

import numpy as np

from ..domain.clustering import Lloyd
from ..domain.discovery import DEFAULT_SUGGEST_THRESHOLD, RelatedNote, related_notes
from ..domain.notes import Note
from ..domain.search import normalize, recall_at_k
from ..domain.text import top_terms
from .errors import DeadlineExceeded, InvalidArgument, NotFound
from .events import (
    BlocksChanged,
    Event,
    Ignored,
    LinksChanged,
    NoteCreated,
    NotePurged,
    NoteRenamed,
    NoteRestored,
    NoteTrashed,
)
from .ports import Cache, Embedder, EmbeddingRepository, NoteRepository, ProcessedEvents
from .vectors import BlockKey, IndexKind, IndexSettings, WorkspaceVectors

__all__ = ["Cluster", "MLApp", "SearchHit", "SearchOutcome", "Settings"]

log = logging.getLogger(__name__)


@dataclass(frozen=True)
class Settings:
    """Tunables, from configuration."""

    suggest_threshold: float = DEFAULT_SUGGEST_THRESHOLD
    default_k: int = 10
    max_k: int = 200
    exact_below: int = 2000
    """Unspecified-index queries use brute force below this many blocks, HNSW above."""
    search_cache_seconds: float = 60.0
    embedding_cache_seconds: float = 7 * 24 * 3600.0
    max_clusters: int = 50
    benchmark_max_runs: int = 10
    index: IndexSettings = field(default_factory=IndexSettings)


@dataclass(frozen=True)
class SearchHit:
    """One block found by semantic search."""

    note_id: str
    block_id: str
    cosine: float


@dataclass(frozen=True)
class SearchOutcome:
    """Hits, the recall against an exact scan (when measured), and the latency."""

    hits: list[SearchHit]
    recall_at_k: float
    latency_ms: float
    index: IndexKind
    cached: bool = False


@dataclass(frozen=True)
class Cluster:
    """A topic cluster of a workspace's notes."""

    id: int
    label: str
    top_terms: list[str]
    note_ids: list[str]


class MLApp:
    """Use cases of the ml service."""

    def __init__(
        self,
        notes: NoteRepository,
        embeddings: EmbeddingRepository,
        processed: ProcessedEvents,
        cache: Cache,
        embedder: Embedder,
        settings: Settings = Settings(),
    ):
        self.notes = notes
        self.embeddings = embeddings
        self.processed = processed
        self.cache = cache
        self.embedder = embedder
        self.settings = settings
        self._lock = threading.RLock()
        self._workspaces: dict[str, WorkspaceVectors] = {}

    # --- events --------------------------------------------------------------

    def handle(self, event: Event) -> bool:
        """Apply one event idempotently; False if it was a duplicate.

        On failure nothing is recorded and the exception propagates, so the
        queue redelivers the event. Every payload is applied as an upsert,
        so applying one twice (a crash after apply, before record) is safe.
        """
        if self.processed.contains(event.id):
            return False
        with self._lock:
            self._apply(event.payload)
        self.processed.record(event.id)
        return True

    def _apply(self, payload) -> None:
        match payload:
            case NoteCreated(note_id=note_id, workspace_id=workspace, title=title):
                note = self.notes.get(note_id) or Note(note_id)
                if note.workspace_id != workspace:
                    self._forget_workspace(note.workspace_id)
                    self._forget_workspace(workspace)
                note.workspace_id, note.title = workspace, title
                self.notes.put(note)
                self._changed(workspace)
            case NoteRenamed(note_id=note_id, title=title):
                note = self.notes.get(note_id) or Note(note_id)
                note.title = title
                self.notes.put(note)
                self._changed(note.workspace_id)
            case NoteTrashed(note_id=note_id) | NoteRestored(note_id=note_id):
                note = self.notes.get(note_id)
                if note is not None:
                    note.trashed = isinstance(payload, NoteTrashed)
                    self.notes.put(note)
                    self._changed(note.workspace_id)
            case NotePurged(note_id=note_id):
                note = self.notes.get(note_id)
                self.notes.delete(note_id)
                self.embeddings.delete_note(note_id)
                if note is not None:
                    if note.workspace_id in self._workspaces:
                        self._workspaces[note.workspace_id].remove_note(note_id)
                    self._changed(note.workspace_id)
            case BlocksChanged():
                self._apply_blocks(payload)
            case LinksChanged(note_id=note_id, targets=targets):
                note = self.notes.get(note_id) or Note(note_id)
                note.links = tuple(sorted(set(targets)))
                self.notes.put(note)
                self._changed(note.workspace_id)
            case Ignored():
                pass
            case _:
                raise TypeError(f"unknown payload {type(payload).__name__}")

    def _apply_blocks(self, change: BlocksChanged) -> None:
        note = self.notes.get(change.note_id) or Note(change.note_id)
        for block_id in change.removed_block_ids:
            note.blocks.pop(block_id, None)
        for block in change.blocks:
            note.blocks[block.block_id] = block.text
        self.notes.put(note)
        model = self.embedder.model_id
        self.embeddings.delete_blocks(note.note_id, change.removed_block_ids)
        changed = [b for b in change.blocks if b.text.strip()]
        empty = [b.block_id for b in change.blocks if not b.text.strip()]
        vectors = self.embed([b.text for b in changed])
        fresh = {b.block_id: v for b, v in zip(changed, vectors, strict=True)}
        self.embeddings.delete_blocks(note.note_id, empty)
        self.embeddings.put(note.note_id, model, fresh)
        loaded = self._workspaces.get(note.workspace_id)
        if loaded is not None:
            loaded.remove([BlockKey(note.note_id, b) for b in (*change.removed_block_ids, *empty)])
            for block_id, vector in fresh.items():
                loaded.upsert(BlockKey(note.note_id, block_id), vector)
        self._changed(note.workspace_id)
        log.info(
            "indexed blocks",
            extra={
                "note_id": note.note_id,
                "embedded": len(fresh),
                "removed": len(change.removed_block_ids) + len(empty),
            },
        )

    def _changed(self, workspace_id: str) -> None:
        self.cache.invalidate_tags([_workspace_tag(workspace_id)])

    def _forget_workspace(self, workspace_id: str) -> None:
        self._workspaces.pop(workspace_id, None)

    # --- embeddings ------------------------------------------------------------

    def embed(self, texts: Sequence[str]) -> np.ndarray:
        """Embed through the cache; only misses reach the embedder."""
        model = self.embedder.model_id
        out = np.zeros((len(texts), self.embedder.dimension), dtype=np.float32)
        missing: list[int] = []
        for i, text in enumerate(texts):
            cached = self.cache.get(_embedding_key(model, text))
            if cached is not None and len(cached) == 4 * self.embedder.dimension:
                out[i] = np.frombuffer(cached, dtype=np.float32)
            else:
                missing.append(i)
        if missing:
            computed = self.embedder.embed([texts[i] for i in missing])
            for i, vector in zip(missing, computed, strict=True):
                out[i] = vector
                self.cache.set(
                    _embedding_key(model, texts[i]),
                    np.asarray(vector, dtype=np.float32).tobytes(),
                    self.settings.embedding_cache_seconds,
                    [f"model:{model}"],
                )
        return out

    # --- semantic search -----------------------------------------------------

    def semantic_search(
        self,
        workspace_id: str,
        query: str,
        k: int = 0,
        index: IndexKind | None = None,
        tags: Sequence[str] = (),
        measure_recall: bool = False,
    ) -> SearchOutcome:
        """Top-k blocks by meaning, filtered by tags during the search."""
        started = time.perf_counter()
        if not workspace_id:
            raise InvalidArgument("workspace_id is required")
        if not query.strip():
            raise InvalidArgument("query is empty")
        k = self._resolve_k(k)
        wanted = sorted({t.strip().lstrip("#").lower() for t in tags if t.strip()})
        with self._lock:
            vectors = self._workspace(workspace_id)
            kind = index or (
                IndexKind.BRUTE_FORCE
                if len(vectors) < self.settings.exact_below
                else IndexKind.HNSW
            )
            key = _search_key(
                workspace_id, self.embedder.model_id, kind, k, wanted, query, measure_recall
            )
            cached = self.cache.get(key)
            if cached is not None:
                hits, recall = _decode_search(cached)
                return SearchOutcome(hits, recall, _elapsed_ms(started), kind, cached=True)
            allowed = self._searchable_notes(workspace_id, wanted)
            query_vector = self.embed([query])[0]
            found, _ = vectors.search(query_vector, k, kind, allowed)
            recall = 1.0
            if measure_recall and kind != IndexKind.BRUTE_FORCE:
                exact, _ = vectors.search(query_vector, k, IndexKind.BRUTE_FORCE, allowed)
                recall = recall_at_k(
                    np.array([_key_text(b) for b, _ in found], dtype=object),
                    np.array([_key_text(b) for b, _ in exact], dtype=object),
                    k,
                )
            hits = [SearchHit(b.note_id, b.block_id, score) for b, score in found]
            self.cache.set(
                key,
                _encode_search(hits, recall),
                self.settings.search_cache_seconds,
                [_workspace_tag(workspace_id)],
            )
        return SearchOutcome(hits, recall, _elapsed_ms(started), kind)

    def _searchable_notes(self, workspace_id: str, tags: list[str]) -> set[str]:
        required = set(tags)
        return {
            note.note_id
            for note in self.notes.in_workspace(workspace_id)
            if not note.trashed and required <= note.tags
        }

    def _workspace(self, workspace_id: str) -> WorkspaceVectors:
        loaded = self._workspaces.get(workspace_id)
        if loaded is not None:
            return loaded
        vectors = WorkspaceVectors(self.embedder.dimension, self.settings.index)
        model = self.embedder.model_id
        for note in self.notes.in_workspace(workspace_id):
            stored = self.embeddings.get(note.note_id, model)
            missing = [b for b in note.block_ids() if b not in stored and note.blocks[b].strip()]
            if missing:
                # Embeddings from another model, or lost: recompute and store them.
                computed = self.embed([note.blocks[b] for b in missing])
                fresh = dict(zip(missing, computed, strict=True))
                self.embeddings.put(note.note_id, model, fresh)
                stored = {**stored, **fresh}
            for block_id in note.block_ids():
                if block_id in stored:
                    vectors.upsert(BlockKey(note.note_id, block_id), stored[block_id])
        self._workspaces[workspace_id] = vectors
        return vectors

    def _resolve_k(self, k: int) -> int:
        if k < 0:
            raise InvalidArgument("k must not be negative")
        k = k or self.settings.default_k
        if k > self.settings.max_k:
            raise InvalidArgument(f"k must be at most {self.settings.max_k}")
        return k

    # --- discovery -------------------------------------------------------------

    def note_embeddings(self, workspace_id: str) -> dict[str, np.ndarray]:
        """Unit note vectors (normalised mean of block vectors) of live notes with text."""
        with self._lock:
            by_note: dict[str, list[np.ndarray]] = {}
            for key, vector in self._workspace(workspace_id).items():
                by_note.setdefault(key.note_id, []).append(vector)
            trashed = {n.note_id for n in self.notes.in_workspace(workspace_id) if n.trashed}
        return {
            note: normalize(np.mean(rows, axis=0))[0]
            for note, rows in sorted(by_note.items())
            if note not in trashed
        }

    def discover(self, note_id: str, k: int = 0) -> list[RelatedNote]:
        """Related notes with cosine, shared tags and link hops as separate signals."""
        k = self._resolve_k(k)
        note = self.notes.get(note_id)
        if note is None or note.trashed:
            raise NotFound(f"note {note_id} is not indexed")
        embeddings = self.note_embeddings(note.workspace_id)
        if note_id not in embeddings:
            return []
        workspace_notes = self.notes.in_workspace(note.workspace_id)
        tags = {n.note_id: n.tags for n in workspace_notes}
        outbound = {n.note_id: n.links for n in workspace_notes}
        return related_notes(
            note_id, embeddings, tags, outbound, k, self.settings.suggest_threshold
        )

    # --- clusters --------------------------------------------------------------

    def list_clusters(self, workspace_id: str) -> list[Cluster]:
        """Topic clusters of the workspace's notes, labelled by distinctive terms.

        ``k = round(sqrt(n / 2))`` (a common rule of thumb), capped by
        ``max_clusters``; Lloyd with k-means++ seeding, which is the most
        reliable choice at the small sizes a workspace has.
        """
        if not workspace_id:
            raise InvalidArgument("workspace_id is required")
        key = f"clusters:{workspace_id}:{self.embedder.model_id}"
        cached = self.cache.get(key)
        if cached is not None:
            return [Cluster(**c) for c in json.loads(cached)]
        embeddings = self.note_embeddings(workspace_id)
        if not embeddings:
            return []
        ids = list(embeddings)
        matrix = np.stack([embeddings[i] for i in ids]).astype(np.float64)
        k = max(1, min(self.settings.max_clusters, round(math.sqrt(len(ids) / 2))))
        labels = Lloyd(k, seed=self.settings.index.seed).fit(matrix).labels
        members = [sorted(ids[i] for i in np.flatnonzero(labels == c)) for c in range(k)]
        members = sorted((m for m in members if m), key=lambda m: (-len(m), m[0]))
        texts = {n.note_id: n.text() for n in self.notes.in_workspace(workspace_id)}
        terms = top_terms([[texts.get(n, "") for n in m] for m in members])
        clusters = [
            Cluster(id=i, label=", ".join(t[:3]) or f"cluster {i}", top_terms=t, note_ids=m)
            for i, (m, t) in enumerate(zip(members, terms, strict=True))
        ]
        self.cache.set(
            key,
            json.dumps([c.__dict__ for c in clusters]).encode(),
            self.settings.search_cache_seconds,
            [_workspace_tag(workspace_id)],
        )
        return clusters

    # --- benchmarks ------------------------------------------------------------

    def benchmark_clustering(
        self,
        workspace_id: str,
        algorithms: Sequence[str],
        k: int,
        runs: int = 1,
        seed: int = 0,
        include_references: bool = True,
        expired: Callable[[], bool] = lambda: False,
    ):
        """Run each algorithm ``runs`` times on the workspace's note vectors.

        80/20 train/test split by a seeded permutation. Returns one averaged
        `AlgorithmRun` per (algorithm, implementation). ``expired`` is polled
        between runs so a caller's deadline stops the work early.
        """
        from ..bench import harness

        unknown = [a for a in algorithms if a not in harness.ALGORITHMS]
        if unknown:
            raise InvalidArgument(f"unknown algorithms {unknown}")
        runs = max(1, min(runs or 1, self.settings.benchmark_max_runs))
        embeddings = self.note_embeddings(workspace_id)
        if not embeddings:
            raise InvalidArgument(f"workspace {workspace_id} has no embedded notes")
        x = np.stack(list(embeddings.values())).astype(np.float64)
        order = np.random.default_rng(seed).permutation(len(x))
        split = max(1, round(0.8 * len(x)))
        train, test = x[order[:split]], x[order[split:]]
        k = k or max(1, round(math.sqrt(len(train) / 2)))
        if len(train) < k:
            raise InvalidArgument(
                f"the workspace has {len(train)} training notes, fewer than k={k}"
            )
        results = []
        for algorithm in algorithms or harness.ALGORITHMS:
            implementations = ["scratch"]
            if include_references:
                implementations += harness.reference_implementations(algorithm)
            for implementation in implementations:
                measured = []
                for r in range(runs):
                    if expired():
                        raise DeadlineExceeded("deadline passed during the benchmark")
                    outcome = harness.run(algorithm, implementation, train, test, k, seed + r)
                    if outcome is not None:
                        measured.append(outcome)
                if measured:
                    results.append(harness.mean_run(measured))
        return results


def _workspace_tag(workspace_id: str) -> str:
    return f"workspace:{workspace_id}"


def _embedding_key(model: str, text: str) -> str:
    return f"embedding:{model}:{hashlib.sha256(text.encode()).hexdigest()}"


def _search_key(workspace_id, model, kind, k, tags, query, measure_recall) -> str:
    digest = hashlib.sha256(json.dumps([query, tags]).encode()).hexdigest()
    return f"search:{workspace_id}:{model}:{kind}:{k}:{int(measure_recall)}:{digest}"


def _encode_search(hits: list[SearchHit], recall: float) -> bytes:
    return json.dumps(
        {"hits": [[h.note_id, h.block_id, h.cosine] for h in hits], "recall": recall}
    ).encode()


def _decode_search(data: bytes) -> tuple[list[SearchHit], float]:
    decoded = json.loads(data)
    return [SearchHit(*hit) for hit in decoded["hits"]], float(decoded["recall"])


def _key_text(key: BlockKey) -> str:
    return f"{key.note_id}\x00{key.block_id}"


def _elapsed_ms(started: float) -> float:
    return (time.perf_counter() - started) * 1000.0
