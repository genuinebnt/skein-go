"""From-scratch clustering in NumPy (ADR-0015).

Lloyd (random and k-means++ initialisation), Elkan, MacQueen mini-batch,
e-means and Gaussian mixtures by EM. Every algorithm is pure (no I/O, no
global state, seeded), counts its distance computations, and exposes
``check_invariants`` for tests to call after every iteration through the
``observer`` argument of ``fit``.
"""

from .common import (
    DistanceCounter,
    FitResult,
    InvariantViolation,
    count_dead_clusters,
    sum_squared_error,
)
from .e_means import EMeans, Recipe, resolve_recipe
from .elkan import Elkan
from .gmm import GaussianMixture, MixtureResult
from .init import kmeans_plus_plus, random_init
from .lloyd import Lloyd
from .metrics import silhouette
from .minibatch import MiniBatch

__all__ = [
    "DistanceCounter",
    "EMeans",
    "Elkan",
    "FitResult",
    "GaussianMixture",
    "InvariantViolation",
    "Lloyd",
    "MiniBatch",
    "MixtureResult",
    "Recipe",
    "count_dead_clusters",
    "kmeans_plus_plus",
    "random_init",
    "resolve_recipe",
    "silhouette",
    "sum_squared_error",
]
