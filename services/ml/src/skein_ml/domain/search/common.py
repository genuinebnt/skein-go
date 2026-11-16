"""Shared vocabulary for the vector indexes: normalisation, results, recall.

All indexes score by cosine similarity on float32 unit vectors, which is a
plain dot product once rows are normalised. Internal ids are dense
integers in insertion order; mapping them to block references is the
caller's job (see `skein_ml.app`). A filter is a boolean mask over internal
ids (True = may be returned). Removed ids stay in the index as tombstones:
they are never returned, but graph indexes may still route through them.

Ties in score are broken by the lower id, so every index (and the exact
scan used as the recall oracle) orders results the same way.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol

import numpy as np

__all__ = ["Hits", "VectorIndex", "normalize", "recall_at_k", "top_k"]


def normalize(vectors: np.ndarray) -> np.ndarray:
    """Float32 copy with unit-length rows; all-zero rows stay zero."""
    vectors = np.atleast_2d(np.asarray(vectors, dtype=np.float32))
    norms = np.linalg.norm(vectors, axis=1, keepdims=True)
    return np.divide(vectors, norms, out=np.zeros_like(vectors), where=norms > 0)


@dataclass(frozen=True)
class Hits:
    """Search results, best first: internal ids and their cosine similarities."""

    ids: np.ndarray
    scores: np.ndarray
    visited: int = 0
    """Vectors whose distance was computed, a hardware-independent cost measure."""

    def __len__(self) -> int:
        return len(self.ids)


class VectorIndex(Protocol):
    """What the application layer needs from any index."""

    def add(self, vectors: np.ndarray) -> np.ndarray:
        """Insert rows (normalised by the index); returns their new ids."""
        ...

    def remove(self, ids: np.ndarray) -> None:
        """Tombstone ids so they are never returned again."""
        ...

    def search(self, query: np.ndarray, k: int, allowed: np.ndarray | None = None) -> Hits:
        """Best ``k`` live ids for ``query``, restricted to ``allowed`` when given."""
        ...

    def check_invariants(self) -> None:
        """Raise InvariantViolation if the index's structure is inconsistent."""
        ...

    def __len__(self) -> int: ...


def top_k(ids: np.ndarray, scores: np.ndarray, k: int) -> tuple[np.ndarray, np.ndarray]:
    """The ``k`` highest scores, best first, ties to the lower id."""
    if len(ids) > k:
        # Partition first so sorting costs O(k log k); include every id tied
        # with the k-th score so the tie-break below sees all of them.
        kth = np.partition(scores, len(scores) - k)[len(scores) - k]
        keep = scores >= kth
        ids, scores = ids[keep], scores[keep]
    order = np.lexsort((ids, -scores))[:k]
    return ids[order], scores[order]


def recall_at_k(approximate: np.ndarray, exact: np.ndarray, k: int) -> float:
    """Fraction of the exact top-k found by the approximate search.

    When fewer than ``k`` results exist at all (a narrow filter), recall is
    measured against what exists, and is 1.0 when nothing exists.
    """
    truth = set(np.asarray(exact)[:k].tolist())
    if not truth:
        return 1.0
    return len(truth & set(np.asarray(approximate)[:k].tolist())) / len(truth)
