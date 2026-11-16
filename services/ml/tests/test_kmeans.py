"""k-means++ seeding, Lloyd, Elkan and MacQueen mini-batch."""

from __future__ import annotations

import numpy as np
import pytest
from conftest import gaussian_blobs

from skein_ml.domain.clustering import Elkan, Lloyd, MiniBatch, kmeans_plus_plus
from skein_ml.domain.clustering.common import squared_distances


def test_kmeans_plus_plus_picks_k_distinct_data_points(rng):
    x, _ = gaussian_blobs(rng, 500, 4, 6)
    centres = kmeans_plus_plus(x, 12, rng)
    assert centres.shape == (12, 4)
    assert len(np.unique(centres, axis=0)) == 12
    assert all((x == centre).all(axis=1).any() for centre in centres)


def test_kmeans_plus_plus_is_deterministic_for_a_seed(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 300, 3, 5)
    first = kmeans_plus_plus(x, 5, np.random.default_rng(seed))
    second = kmeans_plus_plus(x, 5, np.random.default_rng(seed))
    np.testing.assert_array_equal(first, second)


def test_kmeans_plus_plus_seeds_every_well_separated_blob(rng):
    # D^2 sampling makes a second seed in an already-covered blob very
    # unlikely when blobs are far apart relative to their width.
    x, truth = gaussian_blobs(rng, 2000, 2, 8, spread=1000.0, scale=1.0)
    centres = kmeans_plus_plus(x, 8, rng)
    blob_of_centre = {int(truth[(x == c).all(axis=1)][0]) for c in centres}
    assert len(blob_of_centre) == 8


def test_kmeans_plus_plus_handles_more_clusters_than_distinct_points(rng):
    x = np.repeat(np.array([[0.0, 0.0], [1.0, 1.0]]), 50, axis=0)
    centres = kmeans_plus_plus(x, 3, rng)
    assert centres.shape == (3, 2)
    assert np.isfinite(centres).all()


@pytest.mark.parametrize("init", ["random", "kmeans++"])
def test_lloyd_objective_never_increases(seed, init):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 1500, 5, 12)
    model = Lloyd(12, init=init, seed=seed)
    result = model.fit(x, observer=lambda m: m.check_invariants())
    assert result.iterations >= 1
    assert result.distance_computations >= result.iterations * len(x) * 12


def test_lloyd_stops_with_labels_consistent_with_centroids(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 1000, 3, 6)
    result = Lloyd(6, seed=seed, max_iter=500).fit(x)
    nearest = np.argmin(squared_distances(x, result.centroids), axis=1)
    np.testing.assert_array_equal(nearest, result.labels)


def test_lloyd_rejects_fewer_points_than_clusters():
    with pytest.raises(ValueError, match="cannot form"):
        Lloyd(5).fit(np.zeros((3, 2)))


def test_elkan_matches_lloyd_from_the_same_init_with_fewer_distances(seed):
    rng = np.random.default_rng(seed)
    x, _ = gaussian_blobs(rng, 3000, 8, 20)
    init = kmeans_plus_plus(x, 20, np.random.default_rng(seed))
    lloyd = Lloyd(20, init=init).fit(x)
    elkan_model = Elkan(20, init=init)
    elkan = elkan_model.fit(x, observer=lambda m: m.check_invariants())
    np.testing.assert_array_equal(elkan.labels, lloyd.labels)
    np.testing.assert_allclose(elkan.centroids, lloyd.centroids, rtol=1e-10, atol=1e-10)
    assert elkan.iterations == lloyd.iterations
    assert elkan.distance_computations < lloyd.distance_computations


def test_elkan_bounds_stay_valid_from_a_random_init(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 800, 4, 10, spread=3.0)
    Elkan(10, init="random", seed=seed).fit(x, observer=lambda m: m.check_invariants())


def test_elkan_bound_check_catches_a_corrupted_upper_bound(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 200, 3, 4)
    model = Elkan(4, seed=seed)
    model.fit(x)
    model.upper = model.upper * 0.5
    with pytest.raises(AssertionError, match="upper bound"):
        model.check_invariants()


def test_minibatch_masses_account_for_every_point_seen(seed):
    x, _ = gaussian_blobs(np.random.default_rng(seed), 2000, 6, 10)
    model = MiniBatch(10, batch_size=128, epochs=2.5, seed=seed)
    result = model.fit(x, observer=lambda m: m.check_invariants())
    assert model.points_seen == 5000
    assert result.epochs == 2.5
    assert (result.labels >= 0).all()
