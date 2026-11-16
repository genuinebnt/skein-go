"""MacQueen-style mini-batch K-means.

Each cluster keeps a running sum ``S_j`` and count ``N_j`` of every point
ever assigned to it; a batch adds its own sums and counts
(``S <- S + s``, ``N <- N + n``) and the centroid is ``S / N``. In
mass-and-centroid form that is ``c_j <- c_j + n_j / (N_j + n_j) (mean_j - c_j)``,
a per-cluster step of ``1 / N_j`` that shrinks forever. That shrinking step
is what e-means replaces with an exponential moving average (see
`skein_ml.domain.clustering.e_means`).

The data are walked like e-means (`epoch_rows`): each epoch is a shuffle cut
into batches of ``B`` with the remainder kept, and a fractional budget runs a
partial pass before a full last pass, so the two differ only in the update
rule. Labels are those of the last epoch (no extra assignment pass),
again like e-means. Empty clusters are not re-seeded, so the dead-cluster
count shows what revival buys.

Invariants: masses are non-negative and their sum equals the number of
points processed; centroids are finite.

Porting notes: ``N`` is kept in float64 so the mass sum is exact for any
realistic number of points.
"""

from __future__ import annotations

import math
from collections.abc import Callable

import numpy as np

from .common import (
    DistanceCounter,
    FitResult,
    InvariantViolation,
    assign_nearest,
    check_centroids,
    validate_input,
)
from .e_means import epoch_rows
from .lloyd import Init, initial_centroids

__all__ = ["MiniBatch"]


class MiniBatch:
    """Mini-batch K-means with MacQueen's cumulative update."""

    def __init__(self, k: int, batch_size: int = 1024, epochs: float = 3.0,
                 init: Init = "kmeans++", seed: int = 0):
        if batch_size < 1:
            raise ValueError("batch_size must be at least 1")
        if epochs <= 0:
            raise ValueError("epochs must be positive")
        self.k = k
        self.batch_size = batch_size
        self.epochs = epochs
        self.init = init
        self.seed = seed
        self.counter = DistanceCounter()
        self.centroids: np.ndarray | None = None
        self.mass: np.ndarray | None = None
        self.points_seen = 0

    def fit(self, x: np.ndarray, observer: Callable[[MiniBatch], None] | None = None) -> FitResult:
        """Cluster ``x``; ``observer`` runs after every batch update."""
        x = validate_input(x, self.k).astype(np.float64, copy=False)
        n = len(x)
        rng = np.random.default_rng(self.seed)
        self.counter = DistanceCounter()
        self.centroids = initial_centroids(x, self.k, self.init, rng, self.counter)
        self.mass = np.zeros(self.k, dtype=np.float64)
        self.points_seen = 0
        labels = np.full(n, -1, dtype=np.int64)
        batch = min(self.batch_size, n)
        plan = epoch_rows(n, self.epochs)
        updates = 0
        for epoch, rows in enumerate(plan):
            order = rng.permutation(n)[:rows]
            for begin in range(0, rows, batch):
                index = order[begin:begin + batch]
                batch_labels, _ = assign_nearest(x[index], self.centroids, self.counter)
                if epoch == len(plan) - 1:
                    labels[index] = batch_labels
                self._update(x[index], batch_labels)
                updates += 1
                if observer is not None:
                    observer(self)
        seen = labels >= 0
        objective = float(((x[seen] - self.centroids[labels[seen]]) ** 2).sum())
        return FitResult(centroids=self.centroids, labels=labels, objective=objective,
                         iterations=updates, epochs=sum(plan) / n,
                         distance_computations=self.counter.count)

    def _update(self, points: np.ndarray, labels: np.ndarray) -> None:
        counts = np.bincount(labels, minlength=self.k).astype(np.float64)
        sums = np.zeros_like(self.centroids)
        np.add.at(sums, labels, points)
        touched = counts > 0
        new_mass = self.mass[touched] + counts[touched]
        means = sums[touched] / counts[touched, None]
        step = (counts[touched] / new_mass)[:, None]
        self.centroids[touched] += step * (means - self.centroids[touched])
        self.mass[touched] = new_mass
        self.points_seen += len(points)

    def check_invariants(self) -> None:
        """Masses non-negative and summing to the points seen; centroids finite."""
        if self.centroids is None or self.mass is None:
            return
        check_centroids(self.centroids, self.k, self.centroids.shape[1])
        if np.any(self.mass < 0):
            raise InvariantViolation("a mini-batch cluster mass is negative")
        if not math.isclose(float(self.mass.sum()), self.points_seen, rel_tol=1e-12):
            raise InvariantViolation(
                f"masses sum to {self.mass.sum()}, but {self.points_seen} points were seen")

