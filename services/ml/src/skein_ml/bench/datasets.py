"""Synthetic data sets for the benchmark, seeded and generated in memory.

- `gaussian_blobs`: ``k`` isotropic Gaussians (unit variance) with centres
  uniform in ``[-10, 10]^d`` and equal weights. The true ``k`` is known,
  so it measures whether an algorithm finds the obvious structure.
- `embeddings_like`: unit vectors shaped like sentence embeddings, which
  are not isotropic blobs. Topic sizes follow Zipf's law (a few big topics,
  a long tail), every vector shares a common mean direction (real embedding
  spaces are anisotropic), and each topic spreads along a few random
  directions of its own (low-rank covariance) plus small isotropic noise.
  Then rows are L2-normalised, as the service does.

Neither downloads anything, so the benchmark is reproducible offline.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

__all__ = ["Dataset", "embeddings_like", "gaussian_blobs"]


@dataclass(frozen=True)
class Dataset:
    """A train/test split and the ``k`` the benchmark clusters into."""

    name: str
    description: str
    train: np.ndarray
    test: np.ndarray
    k: int


def gaussian_blobs(n_train: int, n_test: int, d: int, k: int, seed: int) -> Dataset:
    """Equal-weight isotropic blobs; see the module doc."""
    rng = np.random.default_rng(seed)
    centres = rng.uniform(-10.0, 10.0, size=(k, d))
    labels = rng.integers(k, size=n_train + n_test)
    x = centres[labels] + rng.normal(size=(n_train + n_test, d))
    return Dataset(
        f"blobs-d{d}-k{k}",
        f"{k} isotropic Gaussian blobs in {d} dimensions (true k = {k})",
        x[:n_train],
        x[n_train:],
        k,
    )


def embeddings_like(
    n_train: int, n_test: int, d: int, topics: int, seed: int, k: int | None = None, rank: int = 4
) -> Dataset:
    """Anisotropic, Zipf-sized topic clusters on the unit sphere; see the module doc."""
    rng = np.random.default_rng(seed)
    n = n_train + n_test
    weights = 1.0 / np.arange(1, topics + 1) ** 1.1
    labels = rng.choice(topics, size=n, p=weights / weights.sum())
    common = rng.normal(size=d)
    common /= np.linalg.norm(common)
    directions = rng.normal(size=(topics, d))
    directions /= np.linalg.norm(directions, axis=1, keepdims=True)
    centres = 0.6 * common + directions
    spread = rng.normal(size=(topics, rank, d)) / np.sqrt(d)
    coefficients = rng.normal(scale=0.9, size=(n, rank))
    x = (
        centres[labels]
        + np.einsum("nr,nrd->nd", coefficients, spread[labels])
        + rng.normal(scale=0.35 / np.sqrt(d), size=(n, d))
    )
    x /= np.linalg.norm(x, axis=1, keepdims=True)
    k = k or topics
    return Dataset(
        f"embeddings-like-d{d}",
        f"{topics} Zipf-sized anisotropic topics on the unit sphere in {d} dimensions, "
        f"clustered into k = {k}",
        x[:n_train],
        x[n_train:],
        k,
    )
