"""Gaussian mixture EM and the silhouette metric."""

from __future__ import annotations

import importlib.util

import numpy as np
import pytest
from conftest import gaussian_blobs

from skein_ml.domain.clustering import GaussianMixture, silhouette
from skein_ml.domain.clustering.gmm import log_sum_exp


@pytest.mark.parametrize("k", [1, 3, 8])
def test_em_log_likelihood_never_decreases(seed, k):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 1500, 4, 6, spread=4.0)
    model = GaussianMixture(k, tol=1e-10, max_iter=60, seed=seed)
    result = model.fit(x, observer=lambda m: m.check_invariants())
    assert result.iterations >= 2
    assert np.all(np.diff(result.history) >= -1e-8 * np.abs(result.history[:-1]))


def test_em_check_catches_a_falling_log_likelihood(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 300, 2, 3)
    model = GaussianMixture(3, seed=seed)
    model.fit(x)
    model.history = [-1.0, -2.0]
    with pytest.raises(AssertionError, match="fell"):
        model.check_invariants()


def test_em_stays_finite_when_a_coordinate_is_constant(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 500, 3, 4)
    x[:, 1] = 7.0
    result = GaussianMixture(4, seed=seed).fit(x)
    assert np.isfinite(result.log_likelihood)
    assert np.all(result.variances[:, 1] == pytest.approx(1e-6))


def test_em_recovers_well_separated_components(seed):
    rng = np.random.default_rng(seed)
    x, truth = gaussian_blobs(rng, 3000, 3, 4, spread=60.0)
    result = GaussianMixture(4, seed=seed).fit(x)
    # Each fitted component should own one true blob almost entirely.
    purity = sum(np.bincount(truth[result.labels == j]).max(initial=0) for j in range(4))
    assert purity / len(x) > 0.98


def test_log_sum_exp_survives_values_that_underflow_exp():
    values = np.array([[-1e4, -1e4 + np.log(3.0)], [5.0, -np.inf]])
    np.testing.assert_allclose(log_sum_exp(values), [-1e4 + np.log(4.0), 5.0])


@pytest.mark.skipif(importlib.util.find_spec("sklearn") is None, reason="scikit-learn missing")
def test_silhouette_matches_scikit_learn(seed):
    from sklearn.metrics import silhouette_score

    rng = np.random.default_rng(seed)
    x, labels = gaussian_blobs(rng, 600, 5, 7, spread=3.0)
    labels[:3] = 99  # a small extra cluster
    labels[3] = 100  # a singleton, whose silhouette is 0 by definition
    assert silhouette(x, labels, sample_size=None) == pytest.approx(
        silhouette_score(x, labels), abs=1e-9
    )


def test_silhouette_of_one_cluster_is_zero():
    assert silhouette(np.random.default_rng(0).normal(size=(50, 2)), np.zeros(50, int)) == 0.0
