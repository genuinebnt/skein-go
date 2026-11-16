"""Centroid initialisation: uniform random sampling and k-means++.

k-means++ (Arthur and Vassilvitskii, 2007) picks the first centre uniformly
and each next centre with probability proportional to D(x)^2, the squared
distance to the nearest centre chosen so far. That gives an O(log k)
approximation in expectation before any Lloyd iteration.

We use the *greedy* variant, as scikit-learn does: at each step draw
``2 + floor(ln k)`` candidates by D^2 sampling and keep the one that lowers
the potential the most. With ``local_trials=1`` it is the textbook algorithm.

Invariants of the result: ``k`` distinct rows of ``x`` (when ``x`` has at
least ``k`` distinct rows), in the order chosen.

Porting notes: the D^2 sampling uses inverse-CDF lookup on a cumulative sum
(``searchsorted``), which ports directly; the generator is NumPy's PCG64, so
a port reproduces the algorithm, not the exact draws.
"""

from __future__ import annotations

import math

import numpy as np

from .common import DistanceCounter, squared_distances, validate_input

__all__ = ["kmeans_plus_plus", "random_init"]


def random_init(x: np.ndarray, k: int, rng: np.random.Generator) -> np.ndarray:
    """``k`` distinct rows of ``x`` chosen uniformly without replacement."""
    x = validate_input(x, k)
    return x[rng.choice(len(x), size=k, replace=False)].copy()


def kmeans_plus_plus(x: np.ndarray, k: int, rng: np.random.Generator,
                     counter: DistanceCounter | None = None,
                     local_trials: int | None = None) -> np.ndarray:
    """Greedy k-means++ seeding; returns a (k, d) array of rows of ``x``."""
    x = validate_input(x, k)
    n = len(x)
    trials = local_trials if local_trials is not None else 2 + int(math.log(k))
    if trials < 1:
        raise ValueError("local_trials must be at least 1")
    centres = np.empty((k, x.shape[1]), dtype=x.dtype)
    first = int(rng.integers(n))
    centres[0] = x[first]
    nearest = squared_distances(x, centres[:1], counter).ravel()
    potential = nearest.sum()
    for index in range(1, k):
        if potential <= 0:
            # Every point coincides with a chosen centre (duplicated data):
            # D^2 sampling is undefined, so fall back to a uniform pick among
            # the points not yet used.
            remaining = np.setdiff1d(np.arange(n), _rows_equal_to(x, centres[:index]))
            pick = int(rng.choice(remaining)) if len(remaining) else int(rng.integers(n))
            centres[index] = x[pick]
            continue
        cumulative = np.cumsum(nearest)
        draws = rng.random(trials) * cumulative[-1]
        candidates = np.minimum(np.searchsorted(cumulative, draws), n - 1)
        candidate_distances = squared_distances(x, x[candidates], counter)
        updated = np.minimum(nearest[:, None], candidate_distances)
        potentials = updated.sum(axis=0)
        best = int(np.argmin(potentials))
        centres[index] = x[candidates[best]]
        nearest = updated[:, best]
        potential = potentials[best]
    return centres


def _rows_equal_to(x: np.ndarray, centres: np.ndarray) -> np.ndarray:
    matches = (x[:, None, :] == centres[None, :, :]).all(axis=2).any(axis=1)
    return np.flatnonzero(matches)
