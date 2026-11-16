"""``python -m skein_ml.bench``: clustering and search benchmarks to Markdown.

Runs every clustering algorithm from scratch and every installed reference
on two data sets (Gaussian blobs with known k, and an embeddings-like set),
averaging ``--runs`` seeds, then the vector search benchmark, and writes
``docs/ml-benchmarks.md`` (or ``--output``) with the machine's details.
Sizes are small so it finishes in a few minutes on a laptop; ``--quick``
shrinks them further for a smoke run.
"""

from __future__ import annotations

import argparse
import datetime as dt
import importlib.metadata
import os
import platform
import sys
import time
from pathlib import Path

import numpy as np

from . import harness
from .datasets import Dataset, embeddings_like, gaussian_blobs
from .search import SearchRow, run_search_benchmark

_REPO_DOCS = Path(__file__).resolve().parents[5] / "docs" / "ml-benchmarks.md"

_NAMES = {
    "kmeans_random": "Lloyd, random init",
    "kmeans_plus_plus": "Lloyd, k-means++",
    "kmeans_elkan": "Elkan, k-means++",
    "kmeans_mini_batch": "Mini-batch (MacQueen)",
    "gmm_em": "Gaussian mixture (EM, diag)",
    "e_means": "e-means",
}


def main(argv: list[str] | None = None) -> int:
    """Parse arguments, run, write the report; returns the exit code."""
    parser = argparse.ArgumentParser(prog="python -m skein_ml.bench", description=__doc__)
    parser.add_argument("--output", type=Path, default=_REPO_DOCS)
    parser.add_argument("--runs", type=int, default=3, help="seeds averaged per algorithm")
    parser.add_argument("--seed", type=int, default=20261116)
    parser.add_argument("--quick", action="store_true", help="tiny sizes, for a smoke run")
    parser.add_argument("--skip-search", action="store_true")
    parser.add_argument("--faiss-contrib", default="",
                        help="FAISS source contrib/ directory, used only if the installed "
                             "faiss lacks contrib.e_means")
    args = parser.parse_args(argv)
    if args.faiss_contrib:
        os.environ["SKEIN_FAISS_CONTRIB"] = args.faiss_contrib

    scale = 0.1 if args.quick else 1.0
    n_train, n_test = int(20_000 * scale), int(5_000 * scale)
    datasets = [
        gaussian_blobs(n_train, n_test, d=16, k=32, seed=args.seed),
        embeddings_like(n_train, n_test, d=128, topics=64, seed=args.seed),
    ]
    started = time.perf_counter()
    clustering = {}
    for dataset in datasets:
        clustering[dataset.name] = _run_clustering(dataset, args.runs, args.seed)
    search_rows: tuple[list[SearchRow], list[SearchRow]] | None = None
    search_shape = (0, 0, 0)
    if not args.skip_search:
        data = embeddings_like(int(10_000 * scale), max(20, int(200 * scale)), d=64, topics=100,
                               seed=args.seed + 7)
        search_shape = (len(data.train), len(data.test), data.train.shape[1])
        print(f"search: {search_shape[0]} vectors, {search_shape[1]} queries", file=sys.stderr)
        search_rows = run_search_benchmark(data.train, data.test, seed=args.seed)
    elapsed = time.perf_counter() - started
    report = _report(datasets, clustering, search_rows, search_shape, args, elapsed)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(report)
    print(report)
    print(f"wrote {args.output} in {elapsed:.0f} s", file=sys.stderr)
    return 0


def _run_clustering(dataset: Dataset, runs: int, seed: int) -> list[harness.AlgorithmRun]:
    results = []
    for algorithm in harness.ALGORITHMS:
        for implementation in ["scratch", *harness.reference_implementations(algorithm)]:
            measured = []
            for r in range(runs):
                print(f"{dataset.name}: {algorithm} ({implementation}) run {r + 1}/{runs}",
                      file=sys.stderr)
                outcome = harness.run(algorithm, implementation, dataset.train, dataset.test,
                                      dataset.k, seed + r)
                if outcome is not None:
                    measured.append(outcome)
            if measured:
                results.append(harness.mean_run(measured))
    return results


def _versions() -> str:
    names = ["numpy", "scipy", "scikit-learn", "faiss-cpu", "hnswlib"]
    found = []
    for name in names:
        try:
            found.append(f"{name} {importlib.metadata.version(name)}")
        except importlib.metadata.PackageNotFoundError:
            found.append(f"{name} (not installed)")
    return ", ".join(found)


def _cpu() -> str:
    try:
        for line in Path("/proc/cpuinfo").read_text().splitlines():
            if line.startswith("model name"):
                return line.split(":", 1)[1].strip()
    except OSError:
        pass
    return platform.processor() or platform.machine()


def _memory_gib() -> str:
    try:
        pages = os.sysconf("SC_PHYS_PAGES") * os.sysconf("SC_PAGE_SIZE")
        return f"{pages / 2**30:.1f} GiB"
    except (ValueError, OSError, AttributeError):
        return "unknown"


def _number(value: float | None, digits: int = 3) -> str:
    if value is None or (isinstance(value, float) and np.isnan(value)):
        return "-"
    return f"{value:.{digits}g}" if abs(value) < 1e5 else f"{value:.3e}"


def _count(value: int | None) -> str:
    return "-" if value is None else f"{value / 1e6:.1f}M"


def _clustering_table(rows: list[harness.AlgorithmRun]) -> list[str]:
    lines = ["| Algorithm | Impl. | Epochs | Wall s | Distances | Train MSE | Test MSE | "
             "Silhouette | Dead | Test NLL |",
             "|---|---|---:|---:|---:|---:|---:|---:|---:|---:|"]
    for r in rows:
        lines.append(
            f"| {_NAMES[r.algorithm]} | {r.implementation} | {_number(r.epochs)} | "
            f"{r.seconds:.3f} | {_count(r.distance_computations)} | "
            f"{_number(r.train_objective, 5)} | {_number(r.test_objective, 5)} | "
            f"{r.silhouette:.3f} | {_number(r.dead_clusters, 2)} | "
            f"{_number(r.test_negative_log_likelihood, 4)} |")
    notes = sorted({f"{_NAMES[r.algorithm]} ({r.implementation}): {n}"
                    for r in rows for n in r.notes})
    if notes:
        lines += ["", *[f"- {n}" for n in notes]]
    return lines


def _search_table(rows: list[SearchRow]) -> list[str]:
    lines = ["| Index | Impl. | Parameters | Build s | Recall@10 | ms/query | Distances/query |",
             "|---|---|---|---:|---:|---:|---:|"]
    for r in rows:
        lines.append(f"| {r.index} | {r.implementation} | {r.parameters} | "
                     f"{_number(r.build_seconds)} | {r.recall:.3f} | "
                     f"{r.milliseconds_per_query:.3f} | {_number(r.distances_per_query, 4)} |")
    return lines


def _report(datasets, clustering, search_rows, search_shape, args, elapsed) -> str:
    lines = [
        "# ml benchmarks",
        "",
        "Generated by `python -m skein_ml.bench` in `services/ml` "
        f"(seed {args.seed}, {args.runs} run(s) per row, averaged). Regenerate rather than "
        "edit by hand. Methods and caveats: `services/ml/README.md`; e-means notes: "
        "`docs/research/e-means.md`.",
        "",
        "## Machine",
        "",
        f"- Date: {dt.date.today().isoformat()}; total benchmark time {elapsed:.0f} s",
        f"- CPU: {_cpu()}, {os.cpu_count()} logical cores; memory {_memory_gib()}",
        f"- {platform.platform()}; Python {platform.python_version()}",
        f"- {_versions()}",
        f"- BLAS threads: {os.environ.get('OMP_NUM_THREADS', 'library default')}",
        "",
        "## Clustering",
        "",
        "Every row is scored from its returned centroids the same way: MSE is the mean squared "
        "distance from each point to its nearest centroid (train, and held-out test points); "
        "silhouette is on a 2,000-point sample of the training set; dead clusters own fewer "
        "than ten training points. Distances counts point-to-centroid distance evaluations "
        "(scratch implementations count exactly; FAISS rows are exact by construction; "
        "scikit-learn does not expose it). Epochs are passes over the training data. Lloyd "
        "and Elkan stop when no label changes (at most 100 iterations); faiss.Kmeans runs "
        "its fixed 25 iterations; mini-batch and e-means get 3 epochs; both EM implementations "
        "stop when the mean log-likelihood gains less than 1e-3.",
        "",
    ]
    for dataset in datasets:
        lines += [f"### {dataset.name}", "",
                  f"{dataset.description}; {len(dataset.train):,} train and "
                  f"{len(dataset.test):,} test points.", ""]
        lines += _clustering_table(clustering[dataset.name])
        lines.append("")
    if search_rows is not None:
        unfiltered, filtered = search_rows
        lines += ["## Vector search", "",
                  f"Embeddings-like data: {search_shape[0]:,} unit vectors in "
                  f"{search_shape[2]} dimensions, {search_shape[1]} held-out queries, k = 10. "
                  "Recall is against the exact scan. Single-threaded; scratch indexes are "
                  "pure Python plus NumPy, so their latency is not comparable to the C++ "
                  "references, but recall and distances per query are.", ""]
        lines += _search_table(unfiltered)
        lines += ["", "### Filtered search", "",
                  "A random filter allows 5% of the vectors. Filtering during the descent "
                  "keeps traversing excluded nodes but only returns allowed ones (ADR-0010); "
                  "filtering after the search drops the excluded results of an unfiltered "
                  "search with the same ef.", ""]
        lines += _search_table(filtered)
    return "\n".join(lines) + "\n"


if __name__ == "__main__":
    sys.exit(main())
