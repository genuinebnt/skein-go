"""e-means: mini-batch K-means with exponential-moving-average updates.

From "E-Means: Scaling Laws for Mini-Batch K-Means" (Bessa, Mazaré,
Szilvasy et al., Meta FAIR, arXiv 2611.09501, 2026). This is an independent
NumPy reimplementation of the behaviour of the reference code in
``faiss/contrib/e_means`` (``base.py`` and ``e_means.py``, commit ``ebab8f7``,
MIT licence, Copyright Meta Platforms, Inc.); credit for the algorithm, the
recipe and the fitted constants belongs to its authors. Where the paper and
the reference code differ we follow the code, which is what the benchmark
compares against. Notes: ``docs/research/e-means.md``.

Algorithm
---------
Each cluster ``j`` has a mass ``N_j`` and centroid ``c_j`` (``c_j = S_j / N_j``
for an implicit sum ``S_j``). For a batch with per-cluster counts ``n_j`` and
sums ``s_j`` and step ``alpha`` (``gamma = 1 - alpha``):

    N_j <- gamma N_j                                   (every cluster)
    for n_j > 0:  N' = N_j + alpha n_j
                  c_j <- c_j + (alpha n_j / N') (s_j / n_j - c_j);  N_j <- N'

which is ``S <- gamma S + alpha s, N <- gamma N + alpha n``. When
``alpha == 1`` the reference skips the decay, so occupied clusters take
``N_j = n_j`` (their exact batch mean) and empty clusters keep their mass; with
``B = n`` and ``alpha = 1`` one update is exactly one Lloyd iteration.

Run: ``k`` distinct initial points from a seeded permutation; every
``N_j = 2^-16``. Each epoch walks a fresh seeded permutation in batches of
``B``, keeping the remainder batch; fractional budgets add a partial pass
before a full closing pass. ``alpha`` follows a cosine schedule from
``alpha0`` to ``0.1 alpha0`` over all updates. Every 5 updates, clusters with
``N_j < tau mean(N)`` are revived by splitting donors sampled in proportion
to mass (Gumbel top-k), unless ``d <= 2``. The last epoch also accumulates
plain sums and counts; the returned centroids are those exact means.

Recipe (shipped fits, not the paper's Table 2)::

    B_throughput = 0.035134   k^0.134749 n^0.876473 T^0.813344
    B_quality    = 0.00439175 k^0.134749 n^0.876473 T^0.813344
    alpha0       = 0.8244 k^-0.4617 B^0.9003 n^-0.4498 T^-0.1057

``B`` is rounded to the nearest power of two (nearest in ratio, ties up),
capped at the power of two nearest ``n/2`` (throughput) or ``n // 2``
(quality), and at ``n``; "auto" is quality for ``d <= 16``. ``alpha0`` uses
the resolved ``B`` and is clamped to ``[1e-4, 1]``.

Fidelity choices: the model (centroids, masses, batch and closing sums) is
float32 and objectives float64, as in the reference. Initial points, epoch
permutations and donor draws use ``numpy.random.RandomState`` seeded as the
reference does, so a run with the same seed visits the same batches and,
up to float32 assignment ties, produces the same centroids as
``faiss.contrib.e_means.NumpyEmeans``. Assignment is exact brute force in
NumPy rather than ``faiss.knn``.

Invariants (``check_invariants``): masses finite and non-negative, centroids
finite, and the mass total equal to the value implied by the decay and the
rows processed (revival splits mass between donor and child and drops the
dead cluster's leftover).

Porting notes: reproducing the reference draws needs Mersenne Twister with
NumPy's ``permutation`` and ``random_sample``; a Rust port should instead
fix its own generator and conform to its own fixtures.
"""

from __future__ import annotations

import math
from collections.abc import Callable
from dataclasses import dataclass

import numpy as np

from .common import (
    DistanceCounter,
    FitResult,
    InvariantViolation,
    check_centroids,
    squared_distances,
    validate_input,
)

__all__ = [
    "ALPHA_MIN_FACTOR",
    "BATCH_LAWS",
    "CHECK_UNUSED_EVERY",
    "INITIAL_MASS",
    "NO_REVIVAL_MAX_D",
    "SPLIT_EPSILON",
    "UNUSED_THRESHOLD",
    "EMeans",
    "Recipe",
    "batch_from_law",
    "epoch_rows",
    "epoch_seed",
    "resolve_recipe",
    "round_to_power_of_two",
    "scheduled_alpha",
    "step_law",
]

ALPHA_MIN_FACTOR = 0.1
"""The cosine schedule ends at this fraction of ``alpha0``."""
CHECK_UNUSED_EVERY = 5
"""Updates between revival checks."""
INITIAL_MASS = 2.0**-16
"""Starting ``N_j``; the paper uses 1e-10."""
SPLIT_EPSILON = 1e-4
"""Relative coordinate offset ``delta`` applied when splitting a donor."""
UNUSED_THRESHOLD = 0.1
"""Default ``tau``: clusters below ``tau * mean(N)`` are revived."""
NO_REVIVAL_MAX_D = 2
"""Revival is off by default at or below this dimension (it hurt 2-D held-out error)."""
AUTO_QUALITY_MAX_D = 16
"""``batch_law="auto"`` picks the quality law at or below this dimension."""
BATCH_LAWS: dict[str, tuple[float, float, float, float]] = {
    # (coefficient, k exponent, n exponent, T exponent)
    "throughput": (0.035134, 0.134749, 0.876473, 0.813344),
    "quality": (0.00439175, 0.134749, 0.876473, 0.813344),
}
ALPHA_LAW = (0.8244, -0.4617, 0.9003, -0.4498, -0.1057)
"""(coefficient, k, B, n, T exponents) of the step law."""


def round_to_power_of_two(value: float) -> int:
    """Nearest power of two by ratio (``value/low`` vs ``high/value``); ties round up."""
    if value <= 1:
        return 1
    low = 2 ** math.floor(math.log2(value))
    high = 2 * low
    return low if value / low < high / value else high


def batch_from_law(n: int, k: int, epochs: float, law: str) -> int:
    """Batch size ``B`` from a fitted law, rounded and capped."""
    if law not in BATCH_LAWS:
        raise ValueError(f"unknown batch law {law!r}; expected one of {sorted(BATCH_LAWS)}")
    coefficient, k_exp, n_exp, t_exp = BATCH_LAWS[law]
    batch = round_to_power_of_two(coefficient * k**k_exp * n**n_exp * epochs**t_exp)
    # The two caps differ on purpose: each reproduces one of the fitted recipes.
    cap = round_to_power_of_two(n / 2) if law == "throughput" else n // 2
    return max(1, min(batch, cap, n))


def step_law(n: int, k: int, epochs: float, batch_size: int) -> float:
    """Initial step ``alpha0`` for a resolved batch size, clamped to ``[1e-4, 1]``."""
    coefficient, k_exp, b_exp, n_exp, t_exp = ALPHA_LAW
    alpha0 = coefficient * k**k_exp * batch_size**b_exp * n**n_exp * epochs**t_exp
    return min(max(alpha0, 1e-4), 1.0)


def epoch_rows(n: int, epochs: float) -> list[int]:
    """Rows per epoch: whole passes, the fractional prefix, then a full closing pass.

    ``T = 2.5`` gives ``[n, n/2, n]``. The fractional row count rounds half to
    even (Python's ``round``). Below one pass only the prefix runs, and it is
    also the closing pass.
    """
    whole = math.floor(epochs)
    remainder = round((epochs - whole) * n)
    if whole == 0:
        return [remainder]
    return [n] * (whole - 1) + ([remainder] if remainder else []) + [n]


def epoch_seed(seed: int, epoch: int) -> int:
    """Seed of epoch ``epoch``'s permutation, independent of any ambient RNG state."""
    return (seed * 1000003 + epoch) % 2**32


def scheduled_alpha(alpha0: float, step: int, total_steps: int) -> float:
    """Cosine annealing from ``alpha0`` at step 0 to ``0.1 alpha0`` at the last step."""
    fraction = min(step, total_steps) / max(1, total_steps)
    cosine = 0.5 * (1.0 + math.cos(math.pi * fraction))
    return alpha0 * (ALPHA_MIN_FACTOR + (1.0 - ALPHA_MIN_FACTOR) * cosine)


@dataclass(frozen=True)
class Recipe:
    """The resolved plan for one run."""

    batch_size: int
    alpha0: float
    batch_law: str | None
    """The law that chose ``batch_size``, or None when it was given."""
    batch_law_reason: str | None
    epoch_rows: tuple[int, ...]
    epoch_steps: tuple[int, ...]
    unused_threshold: float

    @property
    def total_steps(self) -> int:
        """Updates over the whole run, the length of the cosine schedule."""
        return sum(self.epoch_steps)


def resolve_recipe(
    n: int,
    d: int,
    k: int,
    epochs: float,
    batch_size: int | None = None,
    alpha0: float | None = None,
    batch_law: str = "auto",
    unused_threshold: float | None = None,
) -> Recipe:
    """Turn options and the data shape into a `Recipe`.

    A given ``batch_size`` keeps the step law for ``alpha0``; giving both
    fixes both. ``unused_threshold=None`` means 0.1, or 0 (no revival) when
    ``d <= 2``.
    """
    if n < 1 or k < 1 or epochs <= 0:
        raise ValueError("n and k must be positive and epochs must be > 0")
    law: str | None = None
    reason: str | None = None
    if batch_size is None:
        if batch_law == "auto":
            law = "quality" if d <= AUTO_QUALITY_MAX_D else "throughput"
            reason = f"d {'<=' if law == 'quality' else '>'} {AUTO_QUALITY_MAX_D}"
        else:
            law, reason = batch_law, "requested"
        batch = batch_from_law(n, k, epochs, law)
    else:
        if batch_size < 1:
            raise ValueError("batch_size must be at least 1")
        batch = min(int(batch_size), n)
    alpha = step_law(n, k, epochs, batch) if alpha0 is None else float(alpha0)
    if not 0 < alpha <= 1:
        raise ValueError("alpha0 must be in (0, 1]")
    if unused_threshold is None:
        threshold = 0.0 if d <= NO_REVIVAL_MAX_D else UNUSED_THRESHOLD
    else:
        threshold = float(unused_threshold)
        if not threshold >= 0:
            raise ValueError("unused_threshold must be >= 0 (0 disables revival)")
    rows = epoch_rows(n, epochs)
    steps = tuple((r + batch - 1) // batch for r in rows)
    return Recipe(
        batch_size=batch,
        alpha0=alpha,
        batch_law=law,
        batch_law_reason=reason,
        epoch_rows=tuple(rows),
        epoch_steps=steps,
        unused_threshold=threshold,
    )


class EMeans:
    """e-means clustering. One object per run: call ``fit`` once."""

    def __init__(
        self,
        k: int,
        epochs: float = 5.0,
        batch_size: int | None = None,
        alpha0: float | None = None,
        batch_law: str = "auto",
        seed: int = 1234,
        unused_threshold: float | None = None,
    ):
        self.k = int(k)
        self.epochs = float(epochs)
        self.batch_size = batch_size
        self.alpha0 = alpha0
        self.batch_law = batch_law
        self.seed = int(seed)
        self.unused_threshold = unused_threshold
        self.recipe: Recipe | None = None
        self.counter = DistanceCounter()
        self.centroids: np.ndarray | None = None
        self.mass: np.ndarray | None = None
        self.splits = 0
        self.epoch_objectives: list[float] = []
        self.step = 0
        self._expected_mass = 0.0
        self._split_rng: np.random.RandomState | None = None

    def fit(
        self,
        x: np.ndarray,
        init: np.ndarray | None = None,
        observer: Callable[[EMeans], None] | None = None,
    ) -> FitResult:
        """Cluster ``x``; ``observer`` runs after every update (and its revival)."""
        x = np.ascontiguousarray(validate_input(x, self.k), dtype=np.float32)
        n, d = x.shape
        recipe = resolve_recipe(
            n,
            d,
            self.k,
            self.epochs,
            self.batch_size,
            self.alpha0,
            self.batch_law,
            self.unused_threshold,
        )
        self.recipe = recipe
        self._begin(x, init)
        last = len(recipe.epoch_rows) - 1
        labels = np.full(n, -1, dtype=np.int64)
        closing_sums = np.zeros((self.k, d), dtype=np.float32)
        closing_counts = np.zeros(self.k, dtype=np.float64)
        for epoch, rows in enumerate(recipe.epoch_rows):
            order = np.random.RandomState(epoch_seed(self.seed, epoch)).permutation(n)
            objective = 0.0
            for begin in range(0, rows, recipe.batch_size):
                index = order[begin : min(begin + recipe.batch_size, rows)]
                batch = x[index]
                batch_labels, sums, counts, batch_objective = self._accumulate(batch)
                objective += batch_objective
                if epoch == last:
                    labels[index] = batch_labels
                    closing_sums += sums
                    closing_counts += counts
                self._update(sums, counts, len(index))
                if observer is not None:
                    observer(self)
            self.epoch_objectives.append(objective)
        centroids = self.centroids.copy()
        occupied = closing_counts > 0
        centroids[occupied] = closing_sums[occupied] / closing_counts[occupied, None].astype(
            np.float32
        )
        self.centroids = centroids
        seen = labels >= 0
        objective = float(
            ((x[seen].astype(np.float64) - centroids[labels[seen]].astype(np.float64)) ** 2).sum()
        )
        return FitResult(
            centroids=centroids,
            labels=labels,
            objective=objective,
            iterations=recipe.total_steps,
            epochs=sum(recipe.epoch_rows) / n,
            distance_computations=self.counter.count,
            history=list(self.epoch_objectives),
        )

    def _begin(self, x: np.ndarray, init: np.ndarray | None) -> None:
        n, d = x.shape
        if init is not None:
            init = np.asarray(init, dtype=np.float32)
            if init.shape != (self.k, d):
                raise ValueError(f"initial centroids have shape {init.shape}, want {(self.k, d)}")
            self.centroids = init.copy()
        elif n == self.k:
            self.centroids = x.copy()
        else:
            self.centroids = x[np.random.RandomState(self.seed).permutation(n)[: self.k]].copy()
        self.mass = np.full(self.k, INITIAL_MASS, dtype=np.float32)
        self._expected_mass = float(self.mass.sum(dtype=np.float64))
        self._split_rng = np.random.RandomState(self.seed)
        self.counter = DistanceCounter()
        self.step = 0
        self.splits = 0
        self.epoch_objectives = []

    def _accumulate(self, batch: np.ndarray) -> tuple[np.ndarray, np.ndarray, np.ndarray, float]:
        """Assign one batch against the current snapshot: labels, sums, counts, objective."""
        distances = squared_distances(batch, self.centroids, self.counter)
        labels = np.argmin(distances, axis=1)
        nearest = distances[np.arange(len(batch)), labels]
        counts = np.bincount(labels, minlength=self.k).astype(np.float32)
        sums = np.zeros((self.k, batch.shape[1]), dtype=np.float32)
        np.add.at(sums, labels, batch)
        return labels, sums, counts, float(nearest.sum(dtype=np.float64))

    def _update(self, sums: np.ndarray, counts: np.ndarray, rows: int) -> None:
        """Apply the EMA for one batch, then revive every fifth update."""
        recipe = self.recipe
        alpha = scheduled_alpha(recipe.alpha0, self.step, recipe.total_steps)
        touched = counts > 0
        if alpha == 1:
            # The reference replaces rather than decays at alpha == 1, so
            # untouched clusters keep their mass.
            self._expected_mass = float(rows + self.mass[~touched].sum(dtype=np.float64))
            retained = np.zeros(int(touched.sum()), dtype=np.float32)
        else:
            self.mass *= np.float32(1.0 - alpha)
            self._expected_mass = (1.0 - alpha) * self._expected_mass + alpha * rows
            retained = self.mass[touched]
        new_mass = retained + np.float32(alpha) * counts[touched]
        means = sums[touched] / counts[touched, None]
        weight = np.float32(alpha) * counts[touched] / new_mass
        current = self.centroids[touched]
        moved = current + weight[:, None] * (means - current)
        exact = weight == 1
        moved[exact] = means[exact]
        self.centroids[touched] = moved
        self.mass[touched] = new_mass
        if (self.step + 1) % CHECK_UNUSED_EVERY == 0:
            self.revive()
        self.step += 1

    def revive(self) -> int:
        """Split donors into dead clusters; returns how many were revived.

        Dead: ``N_j < tau * mean(N)``. Donors: live clusters with ``N_j > 0``,
        drawn without replacement in proportion to ``N_j`` by Gumbel top-k
        (largest ``log N_j + Gumbel noise``). Donor ``c`` moves to
        ``c (1 - delta sigma)`` and the dead cluster to ``c (1 + delta sigma)``,
        ``sigma = +1, -1, ...`` across coordinates; each keeps half the mass,
        and whatever mass the dead cluster had left is discarded.
        """
        mass = self.mass
        # float32 throughout, as the reference computes it.
        threshold = np.float32(
            mass.sum(dtype=np.float32) * np.float32(self.recipe.unused_threshold)
        ) / np.float32(self.k)
        dead = mass < threshold
        donors = ~dead & (mass > 0)
        count = min(int(dead.sum()), int(donors.sum()))
        if count == 0:
            return 0
        uniform = np.maximum(self._split_rng.random_sample(self.k), np.finfo(np.float64).tiny)
        keys = np.full(self.k, -np.inf)
        keys[donors] = np.log(mass[donors]) - np.log(-np.log(uniform[donors]))
        source = np.argsort(keys)[-count:][::-1]
        target = np.flatnonzero(dead)[:count]

        d = self.centroids.shape[1]
        sign = np.ones(d, dtype=np.float32)
        sign[1::2] = -1.0
        offset = np.float32(SPLIT_EPSILON) * sign
        donor_position = self.centroids[source] * (1.0 - offset)
        child_position = self.centroids[source] * (1.0 + offset)
        _separate_identical(donor_position, child_position)
        half = mass[source] / np.float32(2.0)
        # The dead cluster's leftover mass is replaced, not added to, so the
        # total drops by exactly that amount; splitting itself conserves mass.
        self._expected_mass -= float(mass[target].sum(dtype=np.float64))
        self.mass[source] = half
        self.mass[target] = half
        self.centroids[source] = donor_position
        self.centroids[target] = child_position
        self.splits += count
        return count

    def check_invariants(self) -> None:
        """Masses finite and non-negative, total consistent with the decay; centroids finite."""
        if self.centroids is None or self.mass is None:
            return
        check_centroids(self.centroids, self.k, self.centroids.shape[1])
        if not np.all(np.isfinite(self.mass)) or np.any(self.mass < 0):
            raise InvariantViolation("an e-means mass is negative or not finite")
        total = float(self.mass.sum(dtype=np.float64))
        if not math.isclose(total, self._expected_mass, rel_tol=1e-4, abs_tol=1e-6):
            raise InvariantViolation(
                f"e-means masses sum to {total}, the decay implies {self._expected_mass}"
            )


def _separate_identical(donor: np.ndarray, child: np.ndarray) -> None:
    """Nudge coordinate 0 of a child that equals its donor (an all-zero centroid).

    Without this the two copies would stay identical forever and one would
    never win a point.
    """
    identical = (donor == child).all(axis=1)
    if not identical.any():
        return
    value = child[:, 0]
    with np.errstate(over="ignore"):
        up = np.nextafter(value, np.float32(np.inf))
    nudged = np.where(np.isfinite(up), up, np.nextafter(value, np.float32(-np.inf)))
    child[:, 0] = np.where(identical, nudged, value)
