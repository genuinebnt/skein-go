"""Elkan's accelerated K-means (Elkan, ICML 2003).

Same iterates as Lloyd from the same initial centroids, but it skips distance
computations that the triangle inequality proves cannot change an
assignment. Per point it keeps an upper bound ``u(x) >= d(x, c(x))`` and a
lower bound ``l(x, c) <= d(x, c)`` for every centroid; per centroid it uses
``s(c) = 1/2 min_{c' != c} d(c, c')``.

One iteration:

1. Compute centroid-to-centroid distances and ``s``.
2. Points with ``u(x) <= s(c(x))`` keep their centroid.
3. For every other point and centroid ``c != c(x)`` with ``u(x) > l(x, c)``
   and ``u(x) > d(c(x), c) / 2``: tighten ``u(x)`` to the exact distance
   (once per iteration), re-test, and only then compute ``d(x, c)``.
4. Move centroids to their means; with ``delta(c)`` the distance moved,
   ``l(x, c) <- max(l(x, c) - delta(c), 0)`` and ``u(x) <- u(x) + delta(c(x))``.

Distances here are Euclidean, not squared: the triangle inequality needs a
metric. The loop processes centroids in index order for all points at once,
which is the same order Elkan's per-point loop uses, vectorised across
points.

Invariants (``check_invariants``): the bounds are valid for the current
centroids, which is exactly what makes skipping safe.

Porting notes: the bounds are an (n, k) float table; memory is the limit,
which is why Hamerly's single-lower-bound variant exists. Same termination
rule as `Lloyd` so the two are comparable iteration for iteration.
"""

from __future__ import annotations

from collections.abc import Callable

import numpy as np

from .common import (
    DistanceCounter,
    FitResult,
    InvariantViolation,
    check_centroids,
    check_labels,
    cluster_means,
    squared_distances,
    validate_input,
)
from .lloyd import Init, initial_centroids

__all__ = ["Elkan"]

# Absolute slack when checking bounds against freshly computed distances.
_BOUND_SLACK = 1e-7


class Elkan:
    """Elkan K-means. ``fit`` mirrors `Lloyd.fit`, including the observer."""

    def __init__(self, k: int, init: Init = "kmeans++", max_iter: int = 100, seed: int = 0):
        self.k = k
        self.init = init
        self.max_iter = max_iter
        self.seed = seed
        self.counter = DistanceCounter()
        self.centroids: np.ndarray | None = None
        self.labels: np.ndarray | None = None
        self.upper: np.ndarray | None = None
        self.lower: np.ndarray | None = None
        self._x: np.ndarray | None = None

    def fit(self, x: np.ndarray, observer: Callable[[Elkan], None] | None = None) -> FitResult:
        """Cluster ``x`` and return the result."""
        x = validate_input(x, self.k).astype(np.float64, copy=False)
        self._x = x
        rng = np.random.default_rng(self.seed)
        self.counter = DistanceCounter()
        centroids = initial_centroids(x, self.k, self.init, rng, self.counter)

        # Iteration 1 is a full assignment, which also initialises the bounds.
        lower = np.sqrt(squared_distances(x, centroids, self.counter))
        labels = np.argmin(lower, axis=1)
        upper = lower[np.arange(len(x)), labels].copy()
        iterations = 1
        history: list[float] = []
        self._publish(centroids, labels, upper, lower)

        while True:
            new_centroids, _ = cluster_means(x, labels, centroids)
            moved = np.sqrt(((new_centroids - centroids) ** 2).sum(axis=1))
            self.counter.add(self.k)
            centroids = new_centroids
            lower = np.maximum(lower - moved[None, :], 0.0)
            upper = upper + moved[labels]
            history.append(float(((x - centroids[labels]) ** 2).sum()))
            self._publish(centroids, labels, upper, lower)
            if observer is not None:
                observer(self)
            if iterations >= self.max_iter:
                break
            changed = self._assign(x, centroids, labels, upper, lower)
            iterations += 1
            self._publish(centroids, labels, upper, lower)
            if not changed:
                break

        objective = float(((x - centroids[labels]) ** 2).sum())
        return FitResult(
            centroids=centroids,
            labels=labels,
            objective=objective,
            iterations=iterations,
            epochs=float(iterations),
            distance_computations=self.counter.count,
            history=history,
        )

    def _assign(
        self,
        x: np.ndarray,
        centroids: np.ndarray,
        labels: np.ndarray,
        upper: np.ndarray,
        lower: np.ndarray,
    ) -> bool:
        """One bound-pruned assignment pass, in place. Returns whether any label changed."""
        k = self.k
        between = np.sqrt(squared_distances(centroids, centroids))
        self.counter.add(k * (k - 1) // 2)
        np.fill_diagonal(between, np.inf)
        separation = 0.5 * between.min(axis=1)
        np.fill_diagonal(between, 0.0)

        active = np.flatnonzero(upper > separation[labels])
        if len(active) == 0:
            return False
        points = x[active]
        assigned = labels[active].copy()
        bound = upper[active].copy()
        point_lower = lower[active]
        stale = np.ones(len(active), dtype=bool)
        before = assigned.copy()

        for centre in range(k):
            candidate = (
                (assigned != centre)
                & (bound > point_lower[:, centre])
                & (bound > 0.5 * between[assigned, centre])
            )
            if not candidate.any():
                continue
            tighten = np.flatnonzero(candidate & stale)
            if len(tighten):
                exact = np.sqrt(((points[tighten] - centroids[assigned[tighten]]) ** 2).sum(axis=1))
                self.counter.add(len(tighten))
                bound[tighten] = exact
                point_lower[tighten, assigned[tighten]] = exact
                stale[tighten] = False
                candidate &= (bound > point_lower[:, centre]) & (
                    bound > 0.5 * between[assigned, centre]
                )
            rows = np.flatnonzero(candidate)
            if len(rows) == 0:
                continue
            distance = np.sqrt(((points[rows] - centroids[centre]) ** 2).sum(axis=1))
            self.counter.add(len(rows))
            point_lower[rows, centre] = distance
            closer = distance < bound[rows]
            assigned[rows[closer]] = centre
            bound[rows[closer]] = distance[closer]

        labels[active] = assigned
        upper[active] = bound
        lower[active] = point_lower
        return bool((assigned != before).any())

    def _publish(self, centroids, labels, upper, lower) -> None:
        self.centroids, self.labels, self.upper, self.lower = centroids, labels, upper, lower

    def check_invariants(self) -> None:
        """Bounds bracket the true distances; centroids and labels are valid."""
        if self.centroids is None or self._x is None:
            return
        n, d = self._x.shape
        check_centroids(self.centroids, self.k, d)
        check_labels(self.labels, n, self.k)
        exact = np.sqrt(squared_distances(self._x, self.centroids))
        own = exact[np.arange(n), self.labels]
        if np.any(self.upper < own - _BOUND_SLACK):
            raise InvariantViolation("an Elkan upper bound is below the true distance")
        if np.any(self.lower > exact + _BOUND_SLACK):
            raise InvariantViolation("an Elkan lower bound is above the true distance")
