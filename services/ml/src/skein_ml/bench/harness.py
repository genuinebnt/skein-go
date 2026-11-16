"""Run clustering algorithms side by side and measure them the same way.

Both the ``python -m skein_ml.bench`` CLI and the ``BenchmarkClustering``
RPC use this module, so the numbers in ``docs/ml-benchmarks.md`` and the ones
the service returns are computed identically.

Every run is scored after the fact from its centroids, never from what the
algorithm reports about itself:

- train and test objective: mean squared distance from each point to its
  *nearest* returned centroid (reassigning, so labels left stale by a
  mini-batch's last epoch do not flatter or penalise it);
- silhouette of that nearest-centroid labelling on a seeded sample;
- dead clusters: centroids that own fewer than ten training points.

Gaussian mixtures are scored the same way with their means as centroids,
and also report their negative mean log-likelihood.

References (scikit-learn, FAISS) are optional: a missing library yields no
row rather than an error. If the installed FAISS lacks ``contrib.e_means``
(it shipped in 1.16.0), the reference is loaded from a source checkout named
by ``SKEIN_FAISS_CONTRIB`` (the directory containing ``e_means/``), and the
result's ``implementation`` says so.
"""

from __future__ import annotations

import importlib
import importlib.util
import os
import sys
import time
from dataclasses import dataclass, field

import numpy as np

from ..domain.clustering import (
    Elkan,
    EMeans,
    GaussianMixture,
    Lloyd,
    MiniBatch,
    count_dead_clusters,
    silhouette,
)
from ..domain.clustering.common import assign_nearest

__all__ = [
    "ALGORITHMS",
    "AlgorithmRun",
    "BenchOptions",
    "load_reference_e_means",
    "mean_run",
    "reference_implementations",
    "run",
    "score",
]

ALGORITHMS = ("kmeans_random", "kmeans_plus_plus", "kmeans_elkan", "kmeans_mini_batch",
              "gmm_em", "e_means")
"""Algorithm names, matching ``ClusteringAlgorithm`` without the prefix (lower case)."""


@dataclass(frozen=True)
class BenchOptions:
    """Budgets shared by scratch and reference runs, so comparisons are like for like."""

    lloyd_max_iter: int = 100
    faiss_kmeans_iterations: int = 25
    mini_batch_size: int = 1024
    mini_batch_epochs: float = 3.0
    e_means_epochs: float = 3.0
    gmm_max_iter: int = 100
    gmm_tol: float = 1e-3
    """Stop when the mean log-likelihood gains less than this (scikit-learn's default)."""
    silhouette_sample: int = 2000


@dataclass
class AlgorithmRun:
    """One measured run (or the mean of several)."""

    algorithm: str
    implementation: str
    seconds: float
    iterations: float
    epochs: float
    distance_computations: int | None
    train_objective: float
    test_objective: float
    silhouette: float
    dead_clusters: float
    negative_log_likelihood: float | None = None
    test_negative_log_likelihood: float | None = None
    train_points: int = 0
    test_points: int = 0
    notes: list[str] = field(default_factory=list)


def score(centroids: np.ndarray, x_train: np.ndarray, x_test: np.ndarray, sample: int, seed: int
          ) -> tuple[float, float, float, int]:
    """(train MSE, test MSE, silhouette, dead clusters) of a centroid table."""
    centroids = np.asarray(centroids, dtype=np.float64)
    labels, train_nearest = assign_nearest(x_train, centroids)
    _, test_nearest = assign_nearest(x_test, centroids) if len(x_test) else (None, np.zeros(1))
    return (float(train_nearest.mean()), float(test_nearest.mean()),
            silhouette(x_train, labels, sample_size=sample, seed=seed),
            count_dead_clusters(labels, len(centroids)))


def run(algorithm: str, implementation: str, x_train: np.ndarray, x_test: np.ndarray, k: int,
        seed: int, options: BenchOptions = BenchOptions()) -> AlgorithmRun | None:
    """Fit and score one algorithm; None when that reference is not installed."""
    runner = _RUNNERS.get((algorithm, implementation))
    if runner is None:
        raise ValueError(f"no {implementation} implementation of {algorithm}")
    x_train = np.ascontiguousarray(x_train, dtype=np.float64)
    x_test = np.ascontiguousarray(x_test, dtype=np.float64)
    try:
        fitted = runner(x_train, x_test, k, seed, options)
    except _Missing:
        return None
    centroids, seconds, iterations, epochs, distances, extra = fitted
    train, test, sil, dead = score(centroids, x_train, x_test, options.silhouette_sample, seed)
    result = AlgorithmRun(algorithm=algorithm, implementation=implementation, seconds=seconds,
                          iterations=iterations, epochs=epochs, distance_computations=distances,
                          train_objective=train, test_objective=test, silhouette=sil,
                          dead_clusters=dead, train_points=len(x_train),
                          test_points=len(x_test))
    result.negative_log_likelihood = extra.get("nll")
    result.test_negative_log_likelihood = extra.get("test_nll")
    result.notes = list(extra.get("notes", ()))
    return result


def mean_run(runs: list[AlgorithmRun]) -> AlgorithmRun:
    """Average several seeds' runs of the same algorithm and implementation."""
    first = runs[0]

    def mean(attribute: str):
        values = [getattr(r, attribute) for r in runs]
        return None if any(v is None for v in values) else float(np.mean(values))

    distances = mean("distance_computations")
    return AlgorithmRun(
        algorithm=first.algorithm, implementation=first.implementation, seconds=mean("seconds"),
        iterations=mean("iterations"), epochs=mean("epochs"),
        distance_computations=None if distances is None else round(distances),
        train_objective=mean("train_objective"), test_objective=mean("test_objective"),
        silhouette=mean("silhouette"), dead_clusters=mean("dead_clusters"),
        negative_log_likelihood=mean("negative_log_likelihood"),
        test_negative_log_likelihood=mean("test_negative_log_likelihood"),
        train_points=first.train_points, test_points=first.test_points,
        notes=sorted({n for r in runs for n in r.notes}))


def reference_implementations(algorithm: str) -> list[str]:
    """Reference implementations of ``algorithm`` whose libraries are importable."""
    available = []
    for (name, implementation), _ in _RUNNERS.items():
        if name != algorithm or implementation == "scratch":
            continue
        library = "sklearn" if implementation == "sklearn" else "faiss"
        if importlib.util.find_spec(library) is not None:
            available.append(implementation)
    return available


class _Missing(Exception):
    """A reference library is not installed."""


def _timed(fit):
    start = time.perf_counter()
    value = fit()
    return value, time.perf_counter() - start


# --- scratch -----------------------------------------------------------------

def _scratch_lloyd(init):
    def runner(x, x_test, k, seed, options):
        result, seconds = _timed(lambda: Lloyd(k, init=init, max_iter=options.lloyd_max_iter,
                                               seed=seed).fit(x))
        return (result.centroids, seconds, result.iterations, result.epochs,
                result.distance_computations, {})
    return runner


def _scratch_elkan(x, x_test, k, seed, options):
    result, seconds = _timed(lambda: Elkan(k, max_iter=options.lloyd_max_iter, seed=seed).fit(x))
    return (result.centroids, seconds, result.iterations, result.epochs,
            result.distance_computations, {})


def _scratch_mini_batch(x, x_test, k, seed, options):
    model = MiniBatch(k, batch_size=options.mini_batch_size, epochs=options.mini_batch_epochs,
                      seed=seed)
    result, seconds = _timed(lambda: model.fit(x))
    return (result.centroids, seconds, result.iterations, result.epochs,
            result.distance_computations, {})


def _scratch_e_means(x, x_test, k, seed, options):
    model = EMeans(k, epochs=options.e_means_epochs, seed=seed)
    result, seconds = _timed(lambda: model.fit(x))
    notes = [f"B={model.recipe.batch_size}, alpha0={model.recipe.alpha0:.3g}, "
             f"{model.recipe.batch_law} law, {model.splits} splits"]
    return (result.centroids, seconds, result.iterations, result.epochs,
            result.distance_computations, {"notes": notes})


def _scratch_gmm(x, x_test, k, seed, options):
    model = GaussianMixture(k, max_iter=options.gmm_max_iter, tol=options.gmm_tol, seed=seed)
    result, seconds = _timed(lambda: model.fit(x))
    extra = {"nll": result.objective, "test_nll": -model.score(x_test) if len(x_test) else None}
    return (result.means, seconds, result.iterations, float(result.iterations),
            result.distance_computations, extra)


# --- references --------------------------------------------------------------

def _sklearn():
    try:
        return importlib.import_module("sklearn.cluster"), importlib.import_module(
            "sklearn.mixture")
    except ImportError as error:
        raise _Missing from error


def _sklearn_kmeans(init, algorithm):
    def runner(x, x_test, k, seed, options):
        cluster, _ = _sklearn()
        model = cluster.KMeans(k, init=init, n_init=1, max_iter=options.lloyd_max_iter,
                               algorithm=algorithm, random_state=seed % 2**32)
        _, seconds = _timed(lambda: model.fit(x))
        return model.cluster_centers_, seconds, model.n_iter_, float(model.n_iter_), None, {}
    return runner


def _sklearn_mini_batch(x, x_test, k, seed, options):
    cluster, _ = _sklearn()
    batch = min(options.mini_batch_size, len(x))
    model = cluster.MiniBatchKMeans(k, batch_size=batch, max_iter=int(options.mini_batch_epochs),
                                    n_init=1, random_state=seed % 2**32)
    _, seconds = _timed(lambda: model.fit(x))
    epochs = model.n_steps_ * batch / len(x)
    notes = ["k-means++ init on a sample, reassigns small clusters, may stop early"]
    return model.cluster_centers_, seconds, model.n_steps_, epochs, None, {"notes": notes}


def _sklearn_gmm(x, x_test, k, seed, options):
    _, mixture = _sklearn()
    model = mixture.GaussianMixture(k, covariance_type="diag", max_iter=options.gmm_max_iter,
                                    tol=options.gmm_tol, random_state=seed % 2**32)
    _, seconds = _timed(lambda: model.fit(x))
    extra = {"nll": -model.score(x), "test_nll": -model.score(x_test) if len(x_test) else None}
    return model.means_, seconds, model.n_iter_, float(model.n_iter_), None, extra


def _faiss():
    try:
        return importlib.import_module("faiss")
    except ImportError as error:
        raise _Missing from error


def _faiss_kmeans(x, x_test, k, seed, options):
    faiss = _faiss()
    data = np.ascontiguousarray(x, dtype=np.float32)
    iterations = options.faiss_kmeans_iterations
    model = faiss.Kmeans(data.shape[1], k, niter=iterations, seed=seed % 2**31, verbose=False,
                         max_points_per_centroid=len(data))
    _, seconds = _timed(lambda: model.train(data))
    # faiss.Kmeans runs every iteration (no early stop) with exact assignment.
    return (model.centroids, seconds, iterations, float(iterations), iterations * len(data) * k,
            {"notes": ["random init, fixed iteration count"]})


def load_reference_e_means() -> tuple[type, str]:
    """``NumpyEmeans`` and where it came from ("faiss" or "faiss-source")."""
    _faiss()
    try:
        module = importlib.import_module("faiss.contrib.e_means")
        return module.NumpyEmeans, "faiss"
    except ImportError:
        pass
    source = os.environ.get("SKEIN_FAISS_CONTRIB", "")
    if not source or not os.path.isdir(os.path.join(source, "e_means")):
        raise _Missing
    # The checkout's package imports "faiss" for assignment, which is the
    # installed wheel; only contrib.e_means itself comes from the checkout.
    if source not in sys.path:
        sys.path.insert(0, source)
    module = importlib.import_module("e_means")
    return module.NumpyEmeans, "faiss-source"


def _faiss_e_means(x, x_test, k, seed, options):
    numpy_emeans, origin = load_reference_e_means()
    data = np.ascontiguousarray(x, dtype=np.float32)
    model = numpy_emeans(data.shape[1], k, nepochs=options.e_means_epochs, seed=seed % 2**31)
    _, seconds = _timed(lambda: model.train(data))
    recipe = model.recipe
    rows = sum(recipe["epoch_rows"])
    notes = [f"B={recipe['batch_size']}, alpha0={recipe['alpha0']:.3g}, "
             f"{recipe['batch_law']} law, {model.n_splits} splits"]
    if origin != "faiss":
        notes.append("reference loaded from a FAISS source checkout")
    return (model.centroids, seconds, recipe["total_steps"], rows / len(data), rows * k,
            {"notes": notes})


_RUNNERS = {
    ("kmeans_random", "scratch"): _scratch_lloyd("random"),
    ("kmeans_random", "sklearn"): _sklearn_kmeans("random", "lloyd"),
    ("kmeans_random", "faiss"): _faiss_kmeans,
    ("kmeans_plus_plus", "scratch"): _scratch_lloyd("kmeans++"),
    ("kmeans_plus_plus", "sklearn"): _sklearn_kmeans("k-means++", "lloyd"),
    ("kmeans_elkan", "scratch"): _scratch_elkan,
    ("kmeans_elkan", "sklearn"): _sklearn_kmeans("k-means++", "elkan"),
    ("kmeans_mini_batch", "scratch"): _scratch_mini_batch,
    ("kmeans_mini_batch", "sklearn"): _sklearn_mini_batch,
    ("gmm_em", "scratch"): _scratch_gmm,
    ("gmm_em", "sklearn"): _sklearn_gmm,
    ("e_means", "scratch"): _scratch_e_means,
    ("e_means", "faiss"): _faiss_e_means,
}
