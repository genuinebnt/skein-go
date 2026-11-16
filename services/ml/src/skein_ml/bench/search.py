"""Vector search benchmark: recall@k against the exact scan, latency, work.

Rows: exact NumPy scan; IVF on scratch K-means cells at several ``nprobe``;
scratch HNSW at several ``efSearch``; and, when installed, hnswlib with the
same ``M`` and ``efConstruction`` and FAISS ``IndexIVFFlat`` with the same
cells count. Recall is measured against the scratch exact scan, which
`tests/test_search.py` checks against a full sort.

The filtered section uses a random 5% filter and compares filtering during
the HNSW descent (ADR-0010) with filtering after an unfiltered search of the
same ``ef``: post-filtering keeps only the ~5% of ``ef`` candidates that
pass, which is why the ADR rules it out.
"""

from __future__ import annotations

import importlib.util
import time
from dataclasses import dataclass

import numpy as np

from ..domain.search import BruteForceIndex, HNSWIndex, IVFIndex, normalize, recall_at_k

__all__ = ["SearchRow", "run_search_benchmark"]


@dataclass
class SearchRow:
    """One configuration's measurements."""

    index: str
    implementation: str
    parameters: str
    build_seconds: float
    recall: float
    milliseconds_per_query: float
    distances_per_query: float | None


def _measure(search, queries, truth, k) -> tuple[float, float]:
    started = time.perf_counter()
    found = [search(q) for q in queries]
    elapsed = (time.perf_counter() - started) * 1000 / len(queries)
    recall = float(np.mean([recall_at_k(f, t, k) for f, t in zip(found, truth, strict=True)]))
    return recall, elapsed


def run_search_benchmark(vectors: np.ndarray, queries: np.ndarray, k: int = 10, m: int = 16,
                         ef_construction: int = 100, seed: int = 0,
                         filter_fraction: float = 0.05) -> tuple[list[SearchRow], list[SearchRow]]:
    """Return (unfiltered rows, filtered rows)."""
    vectors, queries = normalize(vectors), normalize(queries)
    n, d = vectors.shape
    rows: list[SearchRow] = []

    exact = BruteForceIndex(d)
    started = time.perf_counter()
    exact.add(vectors)
    exact_build = time.perf_counter() - started
    truth = [exact.search(q, k).ids for q in queries]
    _, ms = _measure(lambda q: exact.search(q, k).ids, queries, truth, k)
    rows.append(SearchRow("exact scan", "scratch", "-", exact_build, 1.0, ms, float(n)))

    nlist = max(1, round(np.sqrt(n)))
    started = time.perf_counter()
    ivf = IVFIndex.train(vectors, nlist, seed=seed)
    ivf.add(vectors)
    ivf_build = time.perf_counter() - started
    for nprobe in (1, 4, 16):
        ivf.set_nprobe(nprobe)
        work = np.mean([ivf.search(q, k).visited for q in queries])
        recall, ms = _measure(lambda q: ivf.search(q, k).ids, queries, truth, k)
        rows.append(SearchRow("IVF-Flat", "scratch", f"nlist={nlist}, nprobe={nprobe}",
                              ivf_build, recall, ms, float(work)))

    started = time.perf_counter()
    hnsw = HNSWIndex(d, m=m, ef_construction=ef_construction, seed=seed)
    hnsw.add(vectors)
    hnsw_build = time.perf_counter() - started
    for ef in (16, 32, 64, 128):
        work = np.mean([hnsw.search(q, k, ef_search=ef).visited for q in queries])
        recall, ms = _measure(lambda q, ef=ef: hnsw.search(q, k, ef_search=ef).ids, queries,
                              truth, k)
        rows.append(SearchRow("HNSW", "scratch", f"M={m}, efC={ef_construction}, ef={ef}",
                              hnsw_build, recall, ms, float(work)))

    library = None
    if importlib.util.find_spec("hnswlib") is not None:
        import hnswlib

        started = time.perf_counter()
        library = hnswlib.Index(space="ip", dim=d)
        library.init_index(max_elements=n, M=m, ef_construction=ef_construction, random_seed=seed)
        library.set_num_threads(1)
        library.add_items(vectors, np.arange(n))
        library_build = time.perf_counter() - started
        for ef in (16, 32, 64, 128):
            library.set_ef(ef)
            recall, ms = _measure(lambda q: library.knn_query(q, k)[0][0], queries, truth, k)
            rows.append(SearchRow("HNSW", "hnswlib", f"M={m}, efC={ef_construction}, ef={ef}",
                                  library_build, recall, ms, None))

    if importlib.util.find_spec("faiss") is not None:
        import faiss

        faiss.omp_set_num_threads(1)
        started = time.perf_counter()
        quantizer = faiss.IndexFlatL2(d)
        reference_ivf = faiss.IndexIVFFlat(quantizer, d, nlist, faiss.METRIC_INNER_PRODUCT)
        reference_ivf.train(vectors)
        reference_ivf.add(vectors)
        faiss_build = time.perf_counter() - started
        for nprobe in (1, 4, 16):
            reference_ivf.nprobe = nprobe
            recall, ms = _measure(lambda q: reference_ivf.search(q[None, :], k)[1][0], queries,
                                  truth, k)
            rows.append(SearchRow("IVF-Flat", "faiss", f"nlist={nlist}, nprobe={nprobe}",
                                  faiss_build, recall, ms, None))

    filtered = _filtered_rows(vectors, queries, exact, hnsw, library, k, seed, filter_fraction,
                              hnsw_build, m, ef_construction)
    return rows, filtered


def _filtered_rows(vectors, queries, exact, hnsw, library, k, seed, fraction, build, m, efc):
    rng = np.random.default_rng(seed + 1)
    allowed = rng.random(len(vectors)) < fraction
    truth = [exact.search(q, k, allowed).ids for q in queries]
    rows = []
    label = f"{fraction:.0%} of vectors allowed"
    allowed_ids = set(np.flatnonzero(allowed).tolist())
    for ef in (64, 128):
        recall, ms = _measure(lambda q, ef=ef: hnsw.search(q, k, allowed, ef_search=ef).ids,
                              queries, truth, k)
        rows.append(SearchRow("HNSW, filter in descent", "scratch",
                              f"M={m}, efC={efc}, ef={ef}; {label}", build, recall, ms, None))

        def post_filter(q, ef=ef):
            ids = hnsw.search(q, ef, ef_search=ef).ids
            return ids[allowed[ids]][:k]

        recall, ms = _measure(post_filter, queries, truth, k)
        rows.append(SearchRow("HNSW, filter after search", "scratch",
                              f"M={m}, efC={efc}, ef={ef}; {label}", build, recall, ms, None))
        if library is not None:
            library.set_ef(ef)

            def library_filtered(q):
                labels, _ = library.knn_query(q, k, filter=lambda i: i in allowed_ids)
                return labels[0]

            recall, ms = _measure(library_filtered, queries, truth, k)
            rows.append(SearchRow("HNSW, filter in descent", "hnswlib",
                                  f"M={m}, efC={efc}, ef={ef}; {label}", float("nan"), recall,
                                  ms, None))
    return rows
