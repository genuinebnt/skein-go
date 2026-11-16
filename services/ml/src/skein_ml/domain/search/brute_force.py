"""Exact cosine search by scanning every vector.

The oracle for recall measurements, and the right index below a few
thousand vectors, where a matrix-vector product beats any graph walk.
Storage is a growable float32 matrix (capacity doubles), so ``add`` is
amortised O(d) per row.

Invariants: ``len(alive) == count``, stored rows are unit length or zero.
"""

from __future__ import annotations

import numpy as np

from ..clustering.common import InvariantViolation
from .common import Hits, normalize, top_k

__all__ = ["BruteForceIndex"]


class BruteForceIndex:
    """Flat index of normalised float32 rows."""

    def __init__(self, dimension: int):
        self.dimension = dimension
        self._vectors = np.zeros((16, dimension), dtype=np.float32)
        self._alive = np.zeros(16, dtype=bool)
        self.count = 0

    @property
    def vectors(self) -> np.ndarray:
        """Read-only view of the stored unit rows, including tombstones."""
        view = self._vectors[: self.count]
        view.flags.writeable = False
        return view

    @property
    def alive(self) -> np.ndarray:
        """Mask of ids that have not been removed."""
        return self._alive[: self.count].copy()

    def add(self, vectors: np.ndarray) -> np.ndarray:
        """Insert rows; returns their ids."""
        rows = normalize(vectors)
        if rows.shape[1] != self.dimension:
            raise ValueError(f"expected dimension {self.dimension}, got {rows.shape[1]}")
        needed = self.count + len(rows)
        if needed > len(self._vectors):
            capacity = max(needed, 2 * len(self._vectors))
            self._vectors = np.resize(self._vectors, (capacity, self.dimension))
            self._alive = np.concatenate([self._alive, np.zeros(capacity - len(self._alive), bool)])
        ids = np.arange(self.count, needed)
        self._vectors[ids] = rows
        self._alive[ids] = True
        self.count = needed
        return ids

    def remove(self, ids: np.ndarray) -> None:
        """Tombstone ids."""
        ids = np.asarray(ids, dtype=np.int64)
        self._alive[ids[(ids >= 0) & (ids < self.count)]] = False

    def search(self, query: np.ndarray, k: int, allowed: np.ndarray | None = None) -> Hits:
        """Exact top-k by cosine similarity among live, allowed ids."""
        mask = self._alive[: self.count]
        if allowed is not None:
            mask = mask & np.asarray(allowed[: self.count], dtype=bool)
        candidates = np.flatnonzero(mask)
        if k <= 0 or len(candidates) == 0:
            return Hits(np.empty(0, np.int64), np.empty(0, np.float32))
        scores = self._vectors[candidates] @ normalize(query)[0]
        ids, best = top_k(candidates, scores, k)
        return Hits(ids, best, visited=len(candidates))

    def __len__(self) -> int:
        return int(self._alive[: self.count].sum())

    def check_invariants(self) -> None:
        """Every stored row is unit length or zero."""
        norms = np.linalg.norm(self._vectors[: self.count], axis=1)
        if not np.all((np.abs(norms - 1) < 1e-4) | (norms == 0)):
            raise InvariantViolation("a stored vector is not normalised")
        if self._alive[self.count :].any():
            raise InvariantViolation("an id beyond the count is marked alive")
