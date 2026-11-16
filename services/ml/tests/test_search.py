"""Brute-force, IVF and HNSW search, with brute force as the recall oracle."""

from __future__ import annotations

import copy

import numpy as np
import pytest
from conftest import unit_rows

from skein_ml.domain.search import (
    BruteForceIndex,
    HNSWIndex,
    IVFIndex,
    normalize,
    recall_at_k,
)


def _oracle(index: BruteForceIndex, queries: np.ndarray, k: int, allowed=None) -> list[np.ndarray]:
    return [index.search(q, k, allowed).ids for q in queries]


def _mean_recall(index, queries, truth, k, **options) -> float:
    return float(np.mean([recall_at_k(index.search(q, k, **options).ids, t, k)
                          for q, t in zip(queries, truth, strict=True)]))


def test_brute_force_matches_a_full_sort_with_ties_to_the_lower_id(rng):
    x = unit_rows(rng, 400, 16)
    x[10] = x[3]  # an exact tie
    index = BruteForceIndex(16)
    index.add(x)
    index.check_invariants()
    for query in [x[3], *unit_rows(rng, 20, 16)]:
        scores = normalize(x) @ normalize(query)[0]
        expected = np.lexsort((np.arange(len(x)), -scores))[:15]
        np.testing.assert_array_equal(index.search(query, 15).ids, expected)


def test_brute_force_never_returns_removed_or_disallowed_ids(rng):
    x = unit_rows(rng, 300, 8)
    index = BruteForceIndex(8)
    index.add(x)
    index.remove(np.arange(0, 300, 3))
    allowed = rng.random(300) < 0.5
    for query in unit_rows(rng, 20, 8):
        ids = index.search(query, 50, allowed).ids
        assert all(i % 3 and allowed[i] for i in ids)


def test_ivf_with_every_cell_probed_is_exact(rng):
    x = unit_rows(rng, 1500, 24)
    exact = BruteForceIndex(24)
    exact.add(x)
    ivf = IVFIndex.train(x, nlist=20, nprobe=20, seed=1)
    ivf.add(x)
    ivf.check_invariants()
    for query in unit_rows(rng, 25, 24):
        np.testing.assert_array_equal(ivf.search(query, 10).ids, exact.search(query, 10).ids)


def test_ivf_recall_never_drops_as_nprobe_grows(rng):
    x = unit_rows(rng, 2000, 24)
    queries = unit_rows(rng, 40, 24)
    exact = BruteForceIndex(24)
    exact.add(x)
    truth = _oracle(exact, queries, 10)
    ivf = IVFIndex.train(x, nlist=32, seed=2)
    ivf.add(x)
    previous = [0.0] * len(queries)
    for nprobe in (1, 2, 4, 8, 16, 32):
        ivf.set_nprobe(nprobe)
        current = [recall_at_k(ivf.search(q, 10).ids, t, 10)
                   for q, t in zip(queries, truth, strict=True)]
        assert all(c >= p for c, p in zip(current, previous, strict=True))
        previous = current
    assert np.mean(previous) == 1.0


def test_ivf_invariants_hold_after_incremental_adds(rng):
    x = unit_rows(rng, 600, 12)
    ivf = IVFIndex.train(x[:300], nlist=10, seed=3)
    for begin in range(0, 600, 100):
        ivf.add(x[begin:begin + 100])
        ivf.check_invariants()
    assert len(ivf) == 600


def test_hnsw_invariants_hold_after_every_insert(rng):
    index = HNSWIndex(8, m=4, ef_construction=16, seed=int(rng.integers(2**31)))
    for row in unit_rows(rng, 300, 8):
        index.add(row)
        index.check_invariants()
    sizes = index.layer_sizes()
    assert sizes[0] == 300
    assert all(a >= b for a, b in zip(sizes, sizes[1:], strict=False))


@pytest.fixture(scope="module")
def built(module_seed):
    """An HNSW index and the exact index over the same 2,000 vectors, built once.

    Tests must not mutate it; the removal test takes a deep copy.
    """
    rng = np.random.default_rng(module_seed)
    x = unit_rows(rng, 2000, 32)
    index = HNSWIndex(32, m=12, ef_construction=80, seed=module_seed)
    index.add(x)
    exact = BruteForceIndex(32)
    exact.add(x)
    return index, exact, unit_rows(rng, 50, 32)


def test_hnsw_recall_at_10_against_brute_force(built):
    index, exact, queries = built
    index.check_invariants()
    truth = _oracle(exact, queries, 10)
    assert _mean_recall(index, queries, truth, 10, ef_search=100) >= 0.95


def test_hnsw_recall_improves_with_ef_search(built):
    index, exact, queries = built
    truth = _oracle(exact, queries, 10)
    low = _mean_recall(index, queries, truth, 10, ef_search=10)
    high = _mean_recall(index, queries, truth, 10, ef_search=200)
    assert high >= low
    assert high >= 0.98


@pytest.mark.parametrize("selectivity", [0.02, 0.2, 0.7])
def test_hnsw_filters_during_the_descent_without_losing_recall(built, rng, selectivity):
    index, exact, queries = built
    allowed = rng.random(index.count) < selectivity
    truth = _oracle(exact, queries, 10, allowed)
    for query in queries:
        assert all(allowed[i] for i in index.search(query, 10, allowed=allowed).ids)
    # Post-filtering a 2% filter would keep ~0.2 of 10 results; filtering in
    # the descent keeps recall high.
    assert _mean_recall(index, queries, truth, 10, allowed=allowed, ef_search=64) >= 0.9


def test_hnsw_routes_through_removed_nodes_but_never_returns_them(built, rng):
    index, exact, queries = copy.deepcopy(built)
    removed = np.flatnonzero(rng.random(index.count) < 0.3)
    index.remove(removed)
    exact.remove(removed)
    truth = _oracle(exact, queries, 10)
    gone = set(removed.tolist())
    for query in queries:
        assert not gone & set(index.search(query, 10).ids.tolist())
    assert _mean_recall(index, queries, truth, 10, ef_search=100) >= 0.95
    index.check_invariants()


def test_hnsw_invariant_check_catches_an_overfull_neighbour_list(rng):
    index = HNSWIndex(4, m=3, ef_construction=8, seed=1)
    index.add(unit_rows(rng, 60, 4))
    index._links[5][0] = [n for n in range(60) if n != 5][:index.m0 + 1]
    with pytest.raises(AssertionError, match="links on layer 0"):
        index.check_invariants()


def test_hnsw_on_an_empty_index_returns_nothing():
    index = HNSWIndex(4)
    index.check_invariants()
    assert len(index.search(np.ones(4), 5)) == 0


def test_recall_is_measured_against_what_exists():
    assert recall_at_k(np.array([1, 2]), np.array([2]), 10) == 1.0
    assert recall_at_k(np.array([]), np.array([]), 10) == 1.0
    assert recall_at_k(np.array([1, 3, 4]), np.array([1, 2, 3]), 3) == pytest.approx(2 / 3)
