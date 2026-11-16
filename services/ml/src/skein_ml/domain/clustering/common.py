"""Primitives shared by the from-scratch clustering algorithms.

Every algorithm counts its point-to-centroid distance computations through a
`DistanceCounter`, so the benchmark can show what Elkan's pruning or a
mini-batch saves without relying on wall-clock time alone. One "distance
computation" is one squared Euclidean distance between a point and a
centroid (or between two centroids), however it is vectorised.

Squared distances use the expansion ``|x|^2 - 2 x.c + |c|^2`` (one matrix
product), clipped at zero because rounding can make it slightly negative.

Porting notes: the counter is a plain integer field; `InvariantViolation`
maps to a sentinel error in Go or an error enum variant in Rust.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

__all__ = [
    "DistanceCounter",
    "FitResult",
    "InvariantViolation",
    "assign_nearest",
    "check_centroids",
    "check_labels",
    "cluster_means",
    "count_dead_clusters",
    "squared_distances",
    "sum_squared_error",
    "validate_input",
]


class InvariantViolation(AssertionError):
    """Raised by ``check_invariants`` when an algorithm's state is impossible.

    It subclasses AssertionError so pytest reports it as a failed check.
    """


@dataclass
class DistanceCounter:
    """Running count of point-to-centroid distance evaluations."""

    count: int = 0

    def add(self, amount: int) -> None:
        """Record ``amount`` more distance computations."""
        self.count += int(amount)


@dataclass
class FitResult:
    """What every K-means-family algorithm returns.

    ``epochs`` counts passes over the training data (a full-batch Lloyd
    iteration is one epoch); ``objective`` is the training sum of squared
    distances from each point to its assigned centroid, computed with the
    returned labels and centroids.
    """

    centroids: np.ndarray
    labels: np.ndarray
    objective: float
    iterations: int
    epochs: float
    distance_computations: int
    history: list[float] = field(default_factory=list)


def squared_distances(
    x: np.ndarray, centroids: np.ndarray, counter: DistanceCounter | None = None
) -> np.ndarray:
    """Return the (n, k) matrix of squared Euclidean distances."""
    if counter is not None:
        counter.add(len(x) * len(centroids))
    x_norms = np.einsum("ij,ij->i", x, x)[:, None]
    c_norms = np.einsum("ij,ij->i", centroids, centroids)[None, :]
    distances = x_norms - 2.0 * (x @ centroids.T) + c_norms
    np.maximum(distances, 0.0, out=distances)
    return distances


def assign_nearest(
    x: np.ndarray,
    centroids: np.ndarray,
    counter: DistanceCounter | None = None,
    chunk_rows: int = 8192,
) -> tuple[np.ndarray, np.ndarray]:
    """Return (labels, squared distance to the nearest centroid) for every row.

    Chunked so the (n, k) distance matrix never has to fit in memory at once.
    Ties go to the lowest centroid index, as ``np.argmin`` does.
    """
    labels = np.empty(len(x), dtype=np.int64)
    nearest = np.empty(len(x), dtype=np.float64)
    for begin in range(0, len(x), chunk_rows):
        rows = x[begin : begin + chunk_rows]
        distances = squared_distances(rows, centroids, counter)
        chunk_labels = np.argmin(distances, axis=1)
        labels[begin : begin + len(rows)] = chunk_labels
        nearest[begin : begin + len(rows)] = distances[np.arange(len(rows)), chunk_labels]
    return labels, nearest


def cluster_means(
    x: np.ndarray, labels: np.ndarray, previous: np.ndarray
) -> tuple[np.ndarray, np.ndarray]:
    """Return (means, counts); clusters assigned nothing keep their previous position.

    Keeping empty clusters in place (rather than re-seeding them) is what
    plain Lloyd does, and is what the dead-cluster column of the benchmark
    measures.
    """
    k = len(previous)
    counts = np.bincount(labels, minlength=k).astype(np.float64)
    sums = np.zeros_like(previous, dtype=np.float64)
    np.add.at(sums, labels, x)
    means = previous.astype(np.float64, copy=True)
    occupied = counts > 0
    means[occupied] = sums[occupied] / counts[occupied, None]
    return means.astype(previous.dtype, copy=False), counts


def sum_squared_error(x: np.ndarray, centroids: np.ndarray) -> float:
    """Sum of squared distances from each row to its nearest centroid.

    Used for held-out evaluation: it reassigns, so it is fair across
    algorithms whose returned labels may be stale.
    """
    _, nearest = assign_nearest(x, centroids)
    return float(nearest.sum())


def count_dead_clusters(labels: np.ndarray, k: int, minimum_points: int = 10) -> int:
    """Clusters holding fewer than ``minimum_points`` points (the proto's definition)."""
    counts = np.bincount(labels[labels >= 0], minlength=k)
    return int((counts < minimum_points).sum())


def check_centroids(centroids: np.ndarray, k: int, d: int) -> None:
    """Invariants every centroid table must satisfy."""
    if centroids.shape != (k, d):
        raise InvariantViolation(f"centroids have shape {centroids.shape}, want {(k, d)}")
    if not np.all(np.isfinite(centroids)):
        raise InvariantViolation("centroids contain NaN or infinity")


def check_labels(labels: np.ndarray, n: int, k: int) -> None:
    """Labels are one per point and name an existing cluster."""
    if labels.shape != (n,):
        raise InvariantViolation(f"labels have shape {labels.shape}, want {(n,)}")
    if n and (labels.min() < 0 or labels.max() >= k):
        raise InvariantViolation("a label names a cluster that does not exist")


def validate_input(x: np.ndarray, k: int) -> np.ndarray:
    """Return ``x`` as a 2-D float array, rejecting inputs no algorithm can cluster."""
    x = np.asarray(x)
    if x.ndim != 2:
        raise ValueError(f"expected a 2-D array of points, got {x.ndim} dimensions")
    if k < 1:
        raise ValueError("k must be at least 1")
    if len(x) < k:
        raise ValueError(f"cannot form {k} clusters from {len(x)} points")
    if not np.all(np.isfinite(x)):
        raise ValueError("points contain NaN or infinity")
    return x
