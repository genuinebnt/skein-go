"""Shared fixtures: seeded randomness and small synthetic data sets.

Every randomised test takes the ``seed`` fixture. It prints the seed (pytest
shows captured output for failures) and honours ``SKEIN_SEED`` so a failure
replays exactly: ``SKEIN_SEED=1234 pytest tests/test_x.py::test_y``.
"""

from __future__ import annotations

import os
import random

import numpy as np
import pytest


def _fresh_seed() -> int:
    return int(os.environ.get("SKEIN_SEED", random.SystemRandom().randrange(2**31)))


@pytest.fixture
def seed(request: pytest.FixtureRequest) -> int:
    """A logged seed: SKEIN_SEED if set, otherwise a fresh random one."""
    value = _fresh_seed()
    print(f"{request.node.nodeid}: SKEIN_SEED={value}")
    request.node.user_properties.append(("SKEIN_SEED", value))
    return value


@pytest.fixture(scope="module")
def module_seed(request: pytest.FixtureRequest) -> int:
    """A logged seed shared by a module's expensive fixtures (printed at setup)."""
    value = _fresh_seed()
    print(f"{request.module.__name__}: module SKEIN_SEED={value}")
    return value


@pytest.fixture
def rng(seed: int) -> np.random.Generator:
    """A NumPy generator built from the logged seed."""
    return np.random.default_rng(seed)


def gaussian_blobs(
    rng: np.random.Generator, n: int, d: int, k: int, spread: float = 10.0, scale: float = 1.0
) -> tuple[np.ndarray, np.ndarray]:
    """``n`` points around ``k`` random centres in ``d`` dimensions, with true labels."""
    centres = rng.uniform(-spread, spread, size=(k, d))
    labels = rng.integers(k, size=n)
    return centres[labels] + rng.normal(scale=scale, size=(n, d)), labels


def unit_rows(rng: np.random.Generator, n: int, d: int) -> np.ndarray:
    """``n`` random float32 unit vectors, clustered a little so search is non-trivial."""
    x, _ = gaussian_blobs(rng, n, d, max(2, n // 50), spread=1.0, scale=0.4)
    x = x.astype(np.float32)
    return x / np.linalg.norm(x, axis=1, keepdims=True)
