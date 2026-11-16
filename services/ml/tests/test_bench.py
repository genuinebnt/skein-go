"""The benchmark harness and CLI."""

from __future__ import annotations

import importlib
import importlib.util
import os
from pathlib import Path

import numpy as np
import pytest

from skein_ml.bench import harness
from skein_ml.bench.__main__ import main
from skein_ml.bench.datasets import embeddings_like, gaussian_blobs


@pytest.mark.parametrize("algorithm", harness.ALGORITHMS)
def test_every_scratch_algorithm_is_scored_the_same_way(algorithm, seed):
    data = gaussian_blobs(600, 150, d=4, k=5, seed=seed)
    run = harness.run(algorithm, "scratch", data.train, data.test, data.k, seed)
    assert run.train_points == 600 and run.test_points == 150
    assert run.distance_computations > 0 and run.epochs > 0
    assert run.train_objective > 0 and run.test_objective > 0
    assert -1 <= run.silhouette <= 1
    assert 0 <= run.dead_clusters <= data.k
    assert (run.negative_log_likelihood is not None) == (algorithm == "gmm_em")


def test_mean_run_averages_seeds():
    data = gaussian_blobs(300, 50, d=3, k=3, seed=1)
    runs = [harness.run("kmeans_plus_plus", "scratch", data.train, data.test, 3, s) for s in (1, 2)]
    mean = harness.mean_run(runs)
    assert mean.train_objective == pytest.approx(np.mean([r.train_objective for r in runs]))


def test_embeddings_like_rows_are_unit_vectors():
    data = embeddings_like(500, 100, d=32, topics=10, seed=3)
    np.testing.assert_allclose(np.linalg.norm(data.train, axis=1), 1.0, rtol=1e-6)
    assert data.k == 10


_SOURCE = os.environ.get("SKEIN_FAISS_CONTRIB", "/home/claude/ref/faiss/contrib")


@pytest.mark.skipif(
    importlib.util.find_spec("faiss") is None or not Path(_SOURCE, "e_means").is_dir(),
    reason="needs faiss-cpu and a FAISS source checkout",
)
def test_reference_e_means_falls_back_to_a_source_checkout(monkeypatch):
    real_import = importlib.import_module

    def wheel_without_contrib(name, *args):
        if name == "faiss.contrib.e_means":
            raise ImportError("simulated older wheel")
        return real_import(name, *args)

    monkeypatch.setattr(harness.importlib, "import_module", wheel_without_contrib)
    monkeypatch.setenv("SKEIN_FAISS_CONTRIB", _SOURCE)
    reference, origin = harness.load_reference_e_means()
    assert origin == "faiss-source" and reference.__name__ == "NumpyEmeans"


def test_cli_writes_a_markdown_report(tmp_path):
    output = tmp_path / "bench.md"
    assert main(["--quick", "--runs", "1", "--skip-search", "--output", str(output)]) == 0
    report = output.read_text()
    assert "## Machine" in report and "| e-means | scratch |" in report
    assert "blobs-d16-k32" in report and "embeddings-like-d128" in report
