"""Silhouette coefficient (Rousseeuw, 1987), from scratch.

For point ``i`` in cluster ``A``: ``a(i)`` is the mean distance to the other
points of ``A`` and ``b(i)`` the smallest mean distance to the points of any
other cluster; ``s(i) = (b - a) / max(a, b)``, and 0 for singletons. The
score is the mean of ``s(i)``, in ``[-1, 1]``, higher is better.

It is O(n^2), so callers pass ``sample_size`` to score a seeded uniform
sample of points (each still compared against the sample only, as
scikit-learn's ``silhouette_score(sample_size=...)`` does). Undefined with
fewer than two non-empty clusters; we return 0.0 there rather than raising,
because the benchmark reports it for degenerate runs too.
"""

from __future__ import annotations

import numpy as np

from .common import squared_distances

__all__ = ["silhouette"]


def silhouette(
    x: np.ndarray,
    labels: np.ndarray,
    sample_size: int | None = 2000,
    seed: int = 0,
    chunk_rows: int = 1024,
) -> float:
    """Mean silhouette coefficient of ``labels`` (Euclidean distance)."""
    x = np.asarray(x, dtype=np.float64)
    labels = np.asarray(labels)
    keep = labels >= 0
    x, labels = x[keep], labels[keep]
    if sample_size is not None and len(x) > sample_size:
        index = np.random.default_rng(seed).choice(len(x), size=sample_size, replace=False)
        x, labels = x[index], labels[index]
    clusters, dense = np.unique(labels, return_inverse=True)
    if len(clusters) < 2:
        return 0.0
    sizes = np.bincount(dense).astype(np.float64)
    one_hot = np.zeros((len(x), len(clusters)))
    one_hot[np.arange(len(x)), dense] = 1.0
    scores = np.empty(len(x))
    for begin in range(0, len(x), chunk_rows):
        rows = slice(begin, begin + chunk_rows)
        distances = np.sqrt(squared_distances(x[rows], x))
        own = dense[rows]
        index = np.arange(len(own))
        # The expansion leaves rounding noise on the self-distance; it must be 0
        # so that dividing by size - 1 excludes the point itself.
        distances[index, begin + index] = 0.0
        # Sum of distances from each row to each cluster's points.
        totals = distances @ one_hot
        own_size = sizes[own]
        within = np.where(own_size > 1, totals[index, own] / np.maximum(own_size - 1, 1), 0.0)
        others = totals / sizes[None, :]
        others[index, own] = np.inf
        nearest_other = others.min(axis=1)
        denominator = np.maximum(within, nearest_other)
        value = np.where(denominator > 0, (nearest_other - within) / denominator, 0.0)
        scores[rows] = np.where(own_size > 1, value, 0.0)
    return float(scores.mean())
