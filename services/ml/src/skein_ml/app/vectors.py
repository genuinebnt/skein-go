"""Per-workspace block vectors and the indexes built over them.

`WorkspaceVectors` maps ``(note_id, block_id)`` keys to dense internal ids
and keeps three indexes consistent:

- the exact `BruteForceIndex` always (it is the recall oracle and the store);
- `HNSWIndex`, built on first use, then updated incrementally;
- `IVFIndex`, trained on first use and retrained once the live set has
  doubled since training, because cells drawn from a small sample stop
  describing a grown collection.

An updated block is a removal (tombstone) plus an insertion. When
tombstones outnumber live vectors everything is rebuilt from the live set
(compaction), so memory stays within twice the live size.

Invariants (``check_invariants``): every index holds the same number of
ids, every live key maps to a live id whose stored vector is the latest,
tombstoned ids map to no key, and each index's own invariants hold.

Not thread-safe; `MLApp` serialises access with its lock.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from enum import StrEnum

import numpy as np

from ..domain.clustering.common import InvariantViolation
from ..domain.search import BruteForceIndex, Hits, HNSWIndex, IVFIndex

__all__ = ["BlockKey", "IndexKind", "IndexSettings", "WorkspaceVectors"]


class IndexKind(StrEnum):
    """Which index answers a query."""

    BRUTE_FORCE = "brute_force"
    IVF = "ivf"
    HNSW = "hnsw"


@dataclass(frozen=True, order=True)
class BlockKey:
    """A block's identity across notes."""

    note_id: str
    block_id: str


@dataclass(frozen=True)
class IndexSettings:
    """Index parameters, from configuration."""

    hnsw_m: int = 16
    hnsw_ef_construction: int = 100
    hnsw_ef_search: int = 64
    ivf_nprobe: int = 8
    ivf_max_lists: int = 1024
    seed: int = 0


class WorkspaceVectors:
    """The vectors of one workspace and their indexes."""

    def __init__(self, dimension: int, settings: IndexSettings = IndexSettings()):
        self.dimension = dimension
        self.settings = settings
        self._reset()

    def _reset(self) -> None:
        self._exact = BruteForceIndex(self.dimension)
        self._hnsw: HNSWIndex | None = None
        self._ivf: IVFIndex | None = None
        self._ivf_trained_on = 0
        self._key_of: list[BlockKey | None] = []
        self._id_of: dict[BlockKey, int] = {}

    def __len__(self) -> int:
        return len(self._id_of)

    def upsert(self, key: BlockKey, vector: np.ndarray) -> None:
        """Insert or replace one block's vector."""
        self.remove([key])
        new_id = int(self._exact.add(vector)[0])
        if self._hnsw is not None:
            self._hnsw.add(vector)
        if self._ivf is not None:
            self._ivf.add(vector)
        self._key_of.append(key)
        self._id_of[key] = new_id
        if self._ivf is not None and len(self) > 2 * self._ivf_trained_on:
            self._ivf = None  # retrained lazily on the next IVF query

    def remove(self, keys: list[BlockKey]) -> None:
        """Tombstone keys; unknown keys are ignored."""
        ids = [self._id_of.pop(key) for key in keys if key in self._id_of]
        if not ids:
            return
        for internal in ids:
            self._key_of[internal] = None
        array = np.array(ids, dtype=np.int64)
        for index in self._indexes():
            index.remove(array)
        if len(self._key_of) - len(self) > max(len(self), 64):
            self._compact()

    def remove_note(self, note_id: str) -> None:
        """Tombstone every block of a note."""
        self.remove([key for key in self._id_of if key.note_id == note_id])

    def keys(self) -> list[BlockKey]:
        """Live keys, sorted."""
        return sorted(self._id_of)

    def items(self) -> list[tuple[BlockKey, np.ndarray]]:
        """Live keys with their stored unit vectors, sorted by key."""
        stored = self._exact.vectors
        return [(key, stored[self._id_of[key]]) for key in self.keys()]

    def search(self, query: np.ndarray, k: int, kind: IndexKind,
               allowed_notes: set[str] | None = None) -> tuple[list[tuple[BlockKey, float]], Hits]:
        """Top-k blocks from the chosen index, restricted to ``allowed_notes`` if given."""
        mask = None
        if allowed_notes is not None:
            mask = np.fromiter((key is not None and key.note_id in allowed_notes
                                for key in self._key_of), dtype=bool, count=len(self._key_of))
        hits = self._index(kind).search(query, k, mask)
        found = [(self._key_of[i], float(s)) for i, s in zip(hits.ids, hits.scores, strict=True)]
        return found, hits

    def _index(self, kind: IndexKind):
        if kind == IndexKind.BRUTE_FORCE:
            return self._exact
        if kind == IndexKind.HNSW:
            if self._hnsw is None:
                self._hnsw = self._build_hnsw()
            return self._hnsw
        if self._ivf is None:
            self._ivf = self._build_ivf()
        return self._ivf

    def _build_hnsw(self) -> HNSWIndex:
        s = self.settings
        index = HNSWIndex(self.dimension, m=s.hnsw_m, ef_construction=s.hnsw_ef_construction,
                          ef_search=s.hnsw_ef_search, seed=s.seed)
        self._replay(index)
        return index

    def _build_ivf(self) -> IVFIndex:
        live = self._live_ids()
        vectors = self._exact.vectors
        if len(live) == 0:
            index = IVFIndex(np.zeros((1, self.dimension), np.float32), self.settings.ivf_nprobe)
        else:
            nlist = min(self.settings.ivf_max_lists, max(1, round(math.sqrt(len(live)))))
            index = IVFIndex.train(vectors[live], nlist, nprobe=self.settings.ivf_nprobe,
                                   seed=self.settings.seed)
        self._replay(index)
        self._ivf_trained_on = max(1, len(live))
        return index

    def _replay(self, index) -> None:
        """Add every stored row (tombstones too) so internal ids line up across indexes."""
        if self._exact.count:
            index.add(self._exact.vectors)
            dead = np.flatnonzero(~self._exact.alive)
            if len(dead):
                index.remove(dead)

    def _compact(self) -> None:
        live = [(key, vector.copy()) for key, vector in self.items()]
        had_hnsw, had_ivf = self._hnsw is not None, self._ivf is not None
        self._reset()
        for key, vector in live:
            self._id_of[key] = int(self._exact.add(vector)[0])
            self._key_of.append(key)
        if had_hnsw:
            self._hnsw = self._build_hnsw()
        if had_ivf:
            self._ivf = self._build_ivf()

    def _live_ids(self) -> np.ndarray:
        return np.flatnonzero(self._exact.alive)

    def _indexes(self):
        return [index for index in (self._exact, self._hnsw, self._ivf) if index is not None]

    def check_invariants(self) -> None:
        """Keys, ids and every index agree (see module doc)."""
        count = len(self._key_of)
        for index in self._indexes():
            if index.count != count:
                raise InvariantViolation(f"an index holds {index.count} ids, the key table {count}")
            index.check_invariants()
        alive = self._exact.alive
        for key, internal in self._id_of.items():
            if self._key_of[internal] != key or not alive[internal]:
                raise InvariantViolation(f"{key} does not map to its live id")
        if int(alive.sum()) != len(self._id_of):
            raise InvariantViolation("a tombstoned id is still alive, or a live id has no key")
