"""Inverted-file (IVF) index: K-means cells, scan only the nearest ``nprobe``.

Training clusters a sample of vectors into ``nlist`` cells with the
from-scratch K-means (`Lloyd` with k-means++ seeding by default, or
`EMeans`). Each vector lives in the list of its nearest centroid. A query
ranks centroids by distance and scans the lists of the ``nprobe`` closest
exactly. Recall rises with ``nprobe``; ``nprobe == nlist`` is an exact scan.

Assignment uses squared L2 distance to the (not normalised) centroids. For
unit queries that ranks cells by ``|c|^2 - 2 q.c``, which is the quantiser
FAISS's ``IndexIVFFlat`` uses with an L2 coarse quantiser.

Invariants: every live id is in exactly one list, the list of its nearest
centroid; ``1 <= nprobe <= nlist``.

Porting notes: lists are Python lists of ids plus a shared row matrix
(`BruteForceIndex`), so a port can use ``Vec<Vec<u32>>``.
"""

from __future__ import annotations

from typing import Literal

import numpy as np

from ..clustering import EMeans, Lloyd
from ..clustering.common import InvariantViolation, assign_nearest
from .brute_force import BruteForceIndex
from .common import Hits, normalize, top_k

__all__ = ["IVFIndex"]


class IVFIndex:
    """IVF-Flat index with cosine scoring."""

    def __init__(self, centroids: np.ndarray, nprobe: int = 8):
        centroids = np.asarray(centroids, dtype=np.float32)
        self.centroids = centroids
        self.nlist = len(centroids)
        self.nprobe = nprobe
        self._store = BruteForceIndex(centroids.shape[1])
        self._lists: list[list[int]] = [[] for _ in range(self.nlist)]
        self._cell_of: list[int] = []
        self.set_nprobe(nprobe)

    @classmethod
    def train(
        cls,
        vectors: np.ndarray,
        nlist: int,
        nprobe: int = 8,
        seed: int = 0,
        trainer: Literal["lloyd", "emeans"] = "lloyd",
        max_iter: int = 25,
    ) -> IVFIndex:
        """Cluster ``vectors`` into ``nlist`` cells and return an empty index over them."""
        rows = normalize(vectors)
        nlist = max(1, min(nlist, len(rows)))
        if trainer == "emeans":
            centroids = EMeans(nlist, epochs=3, seed=seed).fit(rows).centroids
        else:
            centroids = Lloyd(nlist, max_iter=max_iter, seed=seed).fit(rows).centroids
        return cls(centroids, nprobe=nprobe)

    def set_nprobe(self, nprobe: int) -> None:
        """Change how many cells a query scans."""
        if nprobe < 1:
            raise ValueError("nprobe must be at least 1")
        self.nprobe = min(nprobe, self.nlist)

    def add(self, vectors: np.ndarray) -> np.ndarray:
        """Insert rows into their nearest cells; returns their ids."""
        rows = normalize(vectors)
        ids = self._store.add(rows)
        cells, _ = assign_nearest(rows, self.centroids)
        for row_id, cell in zip(ids.tolist(), cells.tolist(), strict=True):
            self._lists[cell].append(row_id)
            self._cell_of.append(cell)
        return ids

    def remove(self, ids: np.ndarray) -> None:
        """Tombstone ids; they stay in their list until a rebuild."""
        self._store.remove(ids)

    def search(self, query: np.ndarray, k: int, allowed: np.ndarray | None = None) -> Hits:
        """Scan the ``nprobe`` nearest cells exactly."""
        unit = normalize(query)
        cell_distances = (
            np.einsum("ij,ij->i", self.centroids, self.centroids) - 2.0 * self.centroids @ unit[0]
        )
        probe = np.argsort(cell_distances, kind="stable")[: self.nprobe]
        candidates = np.fromiter((i for cell in probe for i in self._lists[cell]), dtype=np.int64)
        if len(candidates):
            mask = self._store.alive[candidates]
            if allowed is not None:
                mask &= np.asarray(allowed, dtype=bool)[candidates]
            candidates = candidates[mask]
        if k <= 0 or len(candidates) == 0:
            return Hits(np.empty(0, np.int64), np.empty(0, np.float32), visited=self.nlist)
        scores = self._store.vectors[candidates] @ unit[0]
        ids, best = top_k(candidates, scores, k)
        return Hits(ids, best, visited=self.nlist + len(candidates))

    def list_sizes(self) -> np.ndarray:
        """Number of stored ids (including tombstones) per cell."""
        return np.array([len(cell) for cell in self._lists])

    @property
    def count(self) -> int:
        """Ids stored, including tombstones."""
        return self._store.count

    def __len__(self) -> int:
        return len(self._store)

    def check_invariants(self) -> None:
        """Every id in exactly one list, the list of its nearest centroid."""
        self._store.check_invariants()
        listed = sorted(i for cell in self._lists for i in cell)
        if listed != list(range(self._store.count)):
            raise InvariantViolation("IVF lists do not hold every id exactly once")
        if self._store.count:
            nearest, _ = assign_nearest(self._store.vectors, self.centroids)
            for cell, ids in enumerate(self._lists):
                if any(int(nearest[i]) != cell for i in ids):
                    raise InvariantViolation(f"an id in IVF list {cell} has a nearer centroid")
        if not 1 <= self.nprobe <= self.nlist:
            raise InvariantViolation("nprobe is outside [1, nlist]")
