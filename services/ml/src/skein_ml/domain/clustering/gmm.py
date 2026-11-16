"""Gaussian mixture with diagonal covariances, fitted by expectation maximisation.

Model: ``p(x) = sum_j w_j N(x; mu_j, diag(sigma_j^2))``.

- E-step: log responsibilities ``log w_j + log N(x; mu_j, sigma_j^2) - log p(x)``,
  with ``log p(x)`` by log-sum-exp so no density underflows to zero in high
  dimension.
- M-step: ``N_j = sum_i r_ij``, ``w_j = N_j / n``, ``mu_j`` the weighted mean,
  ``sigma_j^2`` the weighted variance plus ``reg_covar``. The regulariser
  keeps a component that collapses onto one point (or a constant
  coordinate) from reaching zero variance and an infinite likelihood.

Initialisation: k-means++ seeds, then one M-step from the hard assignment
to the nearest seed (as scikit-learn initialises from K-means labels).
Stops when the mean log-likelihood improves by less than ``tol``.

Invariants (``check_invariants``): weights positive and summing to one,
variances at least ``reg_covar``, parameters finite, and the log-likelihood
history non-decreasing. EM guarantees the last property for the exact
M-step; ``reg_covar`` (1e-6 by default) perturbs it by far less than the
slack the check allows.

Porting notes: the log-density uses the expansion
``sum x^2/s - 2 x.(mu/s) + sum mu^2/s`` (one matrix product per term).
"""

from __future__ import annotations

import math
from collections.abc import Callable
from dataclasses import dataclass, field

import numpy as np

from .common import DistanceCounter, InvariantViolation, assign_nearest, validate_input
from .init import kmeans_plus_plus

__all__ = ["GaussianMixture", "MixtureResult", "log_sum_exp"]

# Relative slack for the monotone log-likelihood check (see module doc).
_MONOTONE_SLACK = 1e-8


def log_sum_exp(values: np.ndarray, axis: int = 1) -> np.ndarray:
    """``log(sum(exp(values)))`` along ``axis`` without overflow or underflow."""
    peak = np.max(values, axis=axis, keepdims=True)
    peak = np.where(np.isfinite(peak), peak, 0.0)
    return (peak + np.log(np.sum(np.exp(values - peak), axis=axis, keepdims=True))).squeeze(axis)


@dataclass
class MixtureResult:
    """A fitted mixture. ``objective`` is the negative mean log-likelihood."""

    means: np.ndarray
    variances: np.ndarray
    weights: np.ndarray
    labels: np.ndarray
    log_likelihood: float
    iterations: int
    distance_computations: int
    history: list[float] = field(default_factory=list)

    @property
    def objective(self) -> float:
        """Negative mean log-likelihood on the training data; lower is better."""
        return -self.log_likelihood


class GaussianMixture:
    """Diagonal-covariance Gaussian mixture."""

    def __init__(self, k: int, max_iter: int = 200, tol: float = 1e-4, reg_covar: float = 1e-6,
                 seed: int = 0):
        if reg_covar <= 0:
            raise ValueError("reg_covar must be positive")
        self.k = k
        self.max_iter = max_iter
        self.tol = tol
        self.reg_covar = reg_covar
        self.seed = seed
        self.means: np.ndarray | None = None
        self.variances: np.ndarray | None = None
        self.weights: np.ndarray | None = None
        self.history: list[float] = []
        self.counter = DistanceCounter()

    def fit(self, x: np.ndarray, observer: Callable[[GaussianMixture], None] | None = None
            ) -> MixtureResult:
        """Fit by EM; ``observer`` runs after every M-step."""
        x = validate_input(x, self.k).astype(np.float64, copy=False)
        n = len(x)
        rng = np.random.default_rng(self.seed)
        self.counter = DistanceCounter()
        self.history = []
        seeds = kmeans_plus_plus(x, self.k, rng, self.counter)
        nearest, _ = assign_nearest(x, seeds, self.counter)
        responsibilities = np.zeros((n, self.k))
        responsibilities[np.arange(n), nearest] = 1.0
        self._m_step(x, responsibilities)

        iterations = 0
        log_likelihood = -math.inf
        for _ in range(self.max_iter):
            log_resp, mean_ll = self._e_step(x)
            iterations += 1
            self.history.append(mean_ll)
            converged = abs(mean_ll - log_likelihood) < self.tol
            log_likelihood = mean_ll
            if converged:
                break
            self._m_step(x, np.exp(log_resp))
            if observer is not None:
                observer(self)
        log_resp, log_likelihood = self._e_step(x)
        return MixtureResult(means=self.means, variances=self.variances, weights=self.weights,
                             labels=np.argmax(log_resp, axis=1), log_likelihood=log_likelihood,
                             iterations=iterations, distance_computations=self.counter.count,
                             history=list(self.history))

    def log_joint(self, x: np.ndarray) -> np.ndarray:
        """``log w_j + log N(x_i; mu_j, sigma_j^2)`` as an (n, k) matrix."""
        precision = 1.0 / self.variances
        d = x.shape[1]
        quadratic = ((x**2) @ precision.T - 2.0 * x @ (self.means * precision).T
                     + np.sum(self.means**2 * precision, axis=1)[None, :])
        log_det = np.sum(np.log(self.variances), axis=1)
        self.counter.add(len(x) * self.k)
        return (np.log(self.weights)[None, :]
                - 0.5 * (d * math.log(2 * math.pi) + log_det[None, :] + quadratic))

    def score(self, x: np.ndarray) -> float:
        """Mean log-likelihood of ``x`` under the fitted model (for held-out data)."""
        return float(log_sum_exp(self.log_joint(np.asarray(x, dtype=np.float64))).mean())

    def _e_step(self, x: np.ndarray) -> tuple[np.ndarray, float]:
        joint = self.log_joint(x)
        log_px = log_sum_exp(joint)
        return joint - log_px[:, None], float(log_px.mean())

    def _m_step(self, x: np.ndarray, responsibilities: np.ndarray) -> None:
        # The tiny floor keeps a component that won no point from dividing by zero;
        # it then sits at the data mean with a tiny weight rather than vanishing.
        mass = responsibilities.sum(axis=0) + 10 * np.finfo(np.float64).eps
        means = (responsibilities.T @ x) / mass[:, None]
        second = (responsibilities.T @ (x**2)) / mass[:, None]
        variances = np.maximum(second - means**2, 0.0) + self.reg_covar
        self.means, self.variances = means, variances
        self.weights = mass / mass.sum()

    def check_invariants(self) -> None:
        """Weights form a distribution, variances are regularised, likelihood never falls."""
        if self.weights is None:
            return
        if not (np.all(np.isfinite(self.means)) and np.all(np.isfinite(self.variances))):
            raise InvariantViolation("mixture parameters contain NaN or infinity")
        if np.any(self.weights <= 0) or not math.isclose(self.weights.sum(), 1.0, rel_tol=1e-9):
            raise InvariantViolation("mixture weights are not a probability distribution")
        if np.any(self.variances < self.reg_covar * (1 - 1e-12)):
            raise InvariantViolation("a variance fell below reg_covar")
        for previous, current in zip(self.history, self.history[1:], strict=False):
            if current < previous - _MONOTONE_SLACK * max(1.0, abs(previous)):
                raise InvariantViolation(f"EM log-likelihood fell from {previous} to {current}")

