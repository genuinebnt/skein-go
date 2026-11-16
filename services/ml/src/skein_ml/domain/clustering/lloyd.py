"""Lloyd's algorithm (batch K-means) with random or k-means++ initialisation.

Each iteration assigns every point to its nearest centroid (n * k distance
computations) and moves each centroid to the mean of its points. Clusters
that receive no points keep their position. The loop stops when an
assignment pass changes no label, so the returned labels and centroids are
mutually consistent, or after ``max_iter`` passes.

Invariant (checked by ``check_invariants``): the objective after each
iteration is no larger than after the previous one, since both the
assignment and the mean update can only lower the sum of squared distances.

Porting notes: deterministic given the seed and the initial centroids;
``linfa-clustering`` is the Rust equivalent.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Literal

import numpy as np

from .common import (
    DistanceCounter,
    FitResult,
    InvariantViolation,
    assign_nearest,
    check_centroids,
    check_labels,
    cluster_means,
    validate_input,
)
from .init import kmeans_plus_plus, random_init

__all__ = ["Init", "Lloyd", "initial_centroids"]

Init = Literal["random", "kmeans++"] | np.ndarray

# Relative slack for the monotone-objective check: the objective is a float
# sum over n terms, so reassociation alone can move it by a few ulps.
_MONOTONE_SLACK = 1e-9


def initial_centroids(
    x: np.ndarray, k: int, init: Init, rng: np.random.Generator, counter: DistanceCounter
) -> np.ndarray:
    """Resolve an ``init`` option to a (k, d) array of starting centroids."""
    if isinstance(init, np.ndarray):
        if init.shape != (k, x.shape[1]):
            raise ValueError(f"initial centroids have shape {init.shape}, want {(k, x.shape[1])}")
        return init.astype(x.dtype, copy=True)
    if init == "random":
        return random_init(x, k, rng)
    if init == "kmeans++":
        return kmeans_plus_plus(x, k, rng, counter)
    raise ValueError(f"unknown init {init!r}")


class Lloyd:
    """Batch K-means.

    Call ``fit``; an ``observer`` callable, if given, is called with the model
    after every iteration (tests use it to run ``check_invariants``).
    """

    def __init__(self, k: int, init: Init = "kmeans++", max_iter: int = 100, seed: int = 0):
        self.k = k
        self.init = init
        self.max_iter = max_iter
        self.seed = seed
        self.centroids: np.ndarray | None = None
        self.labels: np.ndarray | None = None
        self.history: list[float] = []
        self.counter = DistanceCounter()
        self._n = 0

    def fit(self, x: np.ndarray, observer: Callable[[Lloyd], None] | None = None) -> FitResult:
        """Cluster ``x`` and return the result."""
        x = validate_input(x, self.k).astype(np.float64, copy=False)
        self._n = len(x)
        rng = np.random.default_rng(self.seed)
        self.counter = DistanceCounter()
        self.history = []
        centroids = initial_centroids(x, self.k, self.init, rng, self.counter)
        labels: np.ndarray | None = None
        iterations = 0
        objective = float("inf")
        for _ in range(self.max_iter):
            new_labels, nearest = assign_nearest(x, centroids, self.counter)
            iterations += 1
            if labels is not None and np.array_equal(new_labels, labels):
                objective = float(nearest.sum())
                break
            labels = new_labels
            centroids, _ = cluster_means(x, labels, centroids)
            # The objective of the updated centroids under the current labels,
            # which is what the monotone invariant is about.
            objective = float(((x - centroids[labels]) ** 2).sum())
            self.centroids, self.labels = centroids, labels
            self.history.append(objective)
            if observer is not None:
                observer(self)
        self.centroids, self.labels = centroids, labels
        return FitResult(
            centroids=centroids,
            labels=labels,
            objective=objective,
            iterations=iterations,
            epochs=float(iterations),
            distance_computations=self.counter.count,
            history=list(self.history),
        )

    def check_invariants(self) -> None:
        """Centroids finite, labels valid, objective non-increasing."""
        if self.centroids is None or self.labels is None:
            return
        check_centroids(self.centroids, self.k, self.centroids.shape[1])
        check_labels(self.labels, self._n, self.k)
        for previous, current in zip(self.history, self.history[1:], strict=False):
            if current > previous * (1 + _MONOTONE_SLACK) + 1e-12:
                raise InvariantViolation(f"Lloyd objective rose from {previous} to {current}")
