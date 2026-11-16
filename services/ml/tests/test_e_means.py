"""e-means: recipe, schedule, revival and fidelity to the reference."""

from __future__ import annotations

import importlib.util
import itertools
import math

import numpy as np
import pytest
from conftest import gaussian_blobs

from skein_ml.domain.clustering import EMeans, Lloyd, sum_squared_error
from skein_ml.domain.clustering.e_means import (
    INITIAL_MASS,
    epoch_rows,
    resolve_recipe,
    round_to_power_of_two,
    scheduled_alpha,
)


def test_one_full_batch_update_with_unit_step_is_one_lloyd_iteration(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 1200, 5, 8)
    x = x.astype(np.float32)
    init = x[np.random.default_rng(seed).choice(len(x), 8, replace=False)]
    emeans = EMeans(8, epochs=1, batch_size=len(x), alpha0=1.0, seed=seed)
    result = emeans.fit(x, init=init)
    lloyd = Lloyd(8, init=init.astype(np.float64), max_iter=1).fit(x.astype(np.float64))
    assert emeans.recipe.total_steps == 1
    np.testing.assert_array_equal(result.labels, lloyd.labels)
    np.testing.assert_allclose(result.centroids, lloyd.centroids, rtol=1e-5, atol=1e-4)


def _nearest_power_of_two(value: float) -> int:
    # Independent restatement: compare log-distances to the neighbouring powers.
    if value <= 1:
        return 1
    low = 2 ** math.floor(math.log2(value))
    return low if math.log2(value) - math.log2(low) < math.log2(2 * low) - math.log2(value) \
        else 2 * low


GRID = list(itertools.product([1_000, 30_000, 1_000_000, 50_000_000], [2, 10, 256, 5000],
                              [1, 1.5, 3, 25], [2, 16, 17, 768]))


@pytest.mark.parametrize(("n", "k", "epochs", "d"), GRID)
def test_recipe_matches_the_scaling_law_formulas(n, k, epochs, d):
    recipe = resolve_recipe(n, d, k, epochs)
    law = "quality" if d <= 16 else "throughput"
    coefficient = 0.00439175 if law == "quality" else 0.035134
    raw = coefficient * k**0.134749 * n**0.876473 * epochs**0.813344
    cap = n // 2 if law == "quality" else _nearest_power_of_two(n / 2)
    batch = max(1, min(_nearest_power_of_two(raw), cap, n))
    alpha0 = min(max(0.8244 * k**-0.4617 * batch**0.9003 * n**-0.4498 * epochs**-0.1057,
                     1e-4), 1.0)
    assert recipe.batch_law == law
    assert recipe.batch_size == batch
    assert recipe.alpha0 == pytest.approx(alpha0, rel=1e-12)
    assert recipe.unused_threshold == (0.0 if d <= 2 else 0.1)
    assert sum(recipe.epoch_rows) == round(epochs * n)
    assert recipe.epoch_steps == tuple(math.ceil(r / batch) for r in recipe.epoch_rows)


@pytest.mark.skipif(importlib.util.find_spec("faiss") is None, reason="faiss-cpu not installed")
@pytest.mark.parametrize(("n", "k", "epochs", "d"), GRID[::7])
def test_recipe_agrees_with_the_faiss_reference(n, k, epochs, d):
    from faiss.contrib.e_means import base

    for law in ("quality", "throughput"):
        ours = resolve_recipe(n, d, k, epochs, batch_law=law)
        batch, alpha0 = base._resolve_recipe(n, k, epochs, law, None, None)
        assert (ours.batch_size, ours.alpha0) == (batch, alpha0)
        assert list(ours.epoch_rows) == base._epoch_rows(n, epochs)


def test_power_of_two_rounding_is_by_ratio_and_ties_round_up():
    assert round_to_power_of_two(0.3) == 1
    assert round_to_power_of_two(5.6) == 4  # 5.6/4 = 1.4 < 8/5.6 = 1.43
    assert round_to_power_of_two(5.7) == 8
    assert round_to_power_of_two(math.sqrt(2) * 64) == 128  # exact ratio tie
    assert round_to_power_of_two(1024) == 1024


def test_epoch_rows_runs_the_fraction_before_a_full_closing_pass():
    assert epoch_rows(1000, 2.5) == [1000, 500, 1000]
    assert epoch_rows(1000, 3) == [1000, 1000, 1000]
    assert epoch_rows(1000, 0.25) == [250]
    assert epoch_rows(5, 1.5) == [2, 5]  # 2.5 rounds half to even


def test_step_schedule_is_a_cosine_from_alpha0_to_a_tenth():
    alphas = [scheduled_alpha(0.5, step, 100) for step in range(101)]
    assert alphas[0] == pytest.approx(0.5)
    assert alphas[50] == pytest.approx(0.5 * (0.1 + 0.9 * 0.5))
    assert alphas[-1] == pytest.approx(0.05)
    assert all(a >= b for a, b in itertools.pairwise(alphas))


def test_invariants_hold_after_every_update(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 3000, 12, 25, spread=4.0)
    model = EMeans(25, epochs=2.5, seed=seed)
    result = model.fit(x, observer=lambda m: m.check_invariants())
    assert result.epochs == 2.5
    assert (result.labels >= 0).all()
    assert result.distance_computations == 7500 * 25


def test_runs_are_reproducible_for_a_seed(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 1500, 6, 10)
    first = EMeans(10, epochs=2, seed=seed).fit(x)
    second = EMeans(10, epochs=2, seed=seed).fit(x)
    np.testing.assert_array_equal(first.centroids, second.centroids)
    np.testing.assert_array_equal(first.labels, second.labels)


def _model_with_masses(mass: np.ndarray, d: int, seed: int) -> EMeans:
    """An e-means object mid-run with the given masses, for driving revival directly."""
    x = np.random.default_rng(seed).normal(size=(len(mass) * 4, d)).astype(np.float32)
    model = EMeans(len(mass), epochs=1, seed=seed)
    model.recipe = resolve_recipe(len(x), d, len(mass), 1)
    model._begin(x, None)
    model.mass = mass.astype(np.float32)
    model._expected_mass = float(model.mass.sum(dtype=np.float64))
    return model


def test_revival_leaves_no_cluster_under_threshold_when_heavy_donors_exist(seed):
    mass = np.array([100.0, 90.0, 120.0, 80.0, 1e-3, 0.0, 2.0, INITIAL_MASS])
    model = _model_with_masses(mass, 8, seed)
    revived = model.revive()
    threshold = 0.1 * model.mass.mean()
    assert revived == 4
    assert (model.mass >= threshold).all(), model.mass
    model.check_invariants()


def test_revival_splits_min_of_dead_and_donors_and_only_drops_dead_mass(seed):
    rng = np.random.default_rng(seed)
    for _ in range(50):
        k = int(rng.integers(2, 40))
        mass = np.where(rng.random(k) < 0.4, rng.random(k) * 1e-3, rng.random(k) * 100 + 1)
        model = _model_with_masses(mass, 6, int(rng.integers(2**31)))
        before_mass = model.mass.copy()
        before_centroids = model.centroids.copy()
        threshold = np.float32(model.mass.sum(dtype=np.float32) * np.float32(0.1)) / np.float32(k)
        dead = before_mass < threshold
        donors = ~dead & (before_mass > 0)
        expected = min(int(dead.sum()), int(donors.sum()))
        assert model.revive() == expected
        children = np.flatnonzero(dead)[:expected]
        dropped = before_mass[children].sum(dtype=np.float64)
        assert model.mass.sum(dtype=np.float64) == pytest.approx(
            before_mass.sum(dtype=np.float64) - dropped, rel=1e-6)
        for child in children:
            # A child sits within the split offset of some donor's old position.
            gap = np.abs(before_centroids[donors] - model.centroids[child]).max(axis=1)
            assert (gap <= 2e-4 * np.abs(before_centroids[donors]).max(axis=1) + 1e-6).any()
        if (before_mass[donors] >= 2 * threshold).all() and dead.sum() <= donors.sum():
            assert (model.mass >= threshold).all()
        model.check_invariants()


def test_revival_separates_a_child_from_an_all_zero_donor(seed):
    model = _model_with_masses(np.array([10.0, 0.0]), 4, seed)
    model.centroids[0] = 0.0
    assert model.revive() == 1
    assert not np.array_equal(model.centroids[0], model.centroids[1])


def test_no_revival_in_two_dimensions(seed):
    rng = np.random.default_rng(seed)
    # Many clusters on few blobs: some die, but d <= 2 must never split.
    x, _ = gaussian_blobs(rng, 4000, 2, 3, spread=50.0, scale=0.5)
    model = EMeans(40, epochs=2, seed=seed)
    model.fit(x, observer=lambda m: m.check_invariants())
    assert model.recipe.unused_threshold == 0.0
    assert model.splits == 0
    revived = EMeans(40, epochs=2, seed=seed, unused_threshold=0.1)
    revived.fit(x)
    assert revived.splits > 0


@pytest.mark.skipif(importlib.util.find_spec("faiss") is None, reason="faiss-cpu not installed")
@pytest.mark.parametrize(("n", "d", "k", "epochs"), [(3000, 8, 10, 3), (4000, 32, 24, 1.5),
                                                     (2500, 2, 12, 2)])
def test_final_loss_matches_faiss_contrib_e_means(seed, n, d, k, epochs):
    from faiss.contrib.e_means import NumpyEmeans

    x, _ = gaussian_blobs(np.random.default_rng(seed), n, d, k, spread=5.0)
    x = x.astype(np.float32)
    reference = NumpyEmeans(d, k, nepochs=epochs, seed=seed % 2**31)
    reference_centroids, _ = reference.train(x)
    ours = EMeans(k, epochs=epochs, seed=seed % 2**31).fit(x)
    assert sum_squared_error(x, ours.centroids) == pytest.approx(
        sum_squared_error(x, reference_centroids), rel=1e-3)
