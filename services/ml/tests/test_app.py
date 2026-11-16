"""Use cases over in-memory fakes: indexing, search, discovery, clusters, benchmarks."""

from __future__ import annotations

import numpy as np
import pytest
from tests_support import CountingEmbedder, make_app

from skein_ml.app.errors import InvalidArgument, NotFound
from skein_ml.app.events import (
    BlocksChanged,
    ChangedBlock,
    Event,
    LinksChanged,
    NoteCreated,
    NotePurged,
    NoteRestored,
    NoteTrashed,
)
from skein_ml.app.service import MLApp, Settings
from skein_ml.app.vectors import BlockKey, IndexKind, IndexSettings, WorkspaceVectors

WS = "ws-1"


class Feed:
    """Builds events with fresh ids."""

    def __init__(self, app: MLApp):
        self.app = app
        self.counter = 0

    def send(self, payload) -> bool:
        self.counter += 1
        return self.app.handle(Event(id=f"evt-{self.counter}", payload=payload))

    def note(self, note_id: str, title: str, *blocks: str, workspace: str = WS) -> None:
        self.send(NoteCreated(note_id, workspace, title))
        self.send(
            BlocksChanged(
                note_id, tuple(ChangedBlock(f"b{i}", text) for i, text in enumerate(blocks))
            )
        )


@pytest.fixture
def app() -> MLApp:
    return make_app()


@pytest.fixture
def feed(app) -> Feed:
    feed = Feed(app)
    feed.note(
        "raft",
        "Raft",
        "leader election and log replication in raft #consensus",
        "raft snapshots compact the log #consensus",
    )
    feed.note("paxos", "Paxos", "multi paxos leader and log replication #consensus")
    feed.note("bread", "Bread", "sourdough starter hydration and a long cold proof #baking")
    return feed


def hits(app, query, **options):
    return [(h.note_id, h.block_id) for h in app.semantic_search(WS, query, **options).hits]


@pytest.mark.parametrize("kind", list(IndexKind))
def test_indexed_blocks_are_found_by_every_index(app, feed, kind):
    found = hits(app, "log replication with a leader", k=3, index=kind)
    assert found[0][0] in {"raft", "paxos"}
    assert ("bread", "b0") not in found[:2]


def test_tag_filter_restricts_results_during_the_search(app, feed):
    found = hits(app, "log replication", k=10, tags=["#Baking"], index=IndexKind.HNSW)
    assert found == [("bread", "b0")]


def test_trashed_notes_are_hidden_until_restored(app, feed):
    feed.send(NoteTrashed("raft"))
    assert all(note != "raft" for note, _ in hits(app, "raft snapshots", k=10))
    feed.send(NoteRestored("raft"))
    assert hits(app, "raft snapshots", k=1) == [("raft", "b1")]


def test_purge_erases_the_note_everywhere(app, feed):
    feed.send(NotePurged("raft"))
    assert app.notes.get("raft") is None
    assert app.embeddings.get("raft", app.embedder.model_id) == {}
    assert all(note != "raft" for note, _ in hits(app, "raft snapshots", k=10))


def test_changed_and_removed_blocks_are_reindexed(app, feed):
    feed.send(
        BlocksChanged(
            "raft", (ChangedBlock("b0", "gardening tomatoes in spring"),), removed_block_ids=("b1",)
        )
    )
    assert hits(app, "tomatoes gardening", k=1) == [("raft", "b0")]
    assert ("raft", "b1") not in hits(app, "raft snapshots compact", k=10)


def test_duplicate_events_are_applied_once(app):
    event = Event("same-id", NoteCreated("n", WS, "first"))
    assert app.handle(event) is True
    assert app.handle(Event("same-id", NoteCreated("n", WS, "second"))) is False
    assert app.notes.get("n").title == "first"


def test_a_failed_event_is_not_recorded_so_redelivery_applies_it():
    embedder = CountingEmbedder(fail_next=True)
    app = make_app(embedder)
    app.handle(Event("e1", NoteCreated("n", WS, "t")))
    event = Event("e2", BlocksChanged("n", (ChangedBlock("b", "vector clocks"),)))
    with pytest.raises(RuntimeError):
        app.handle(event)
    assert app.handle(event) is True
    assert hits(app, "vector clocks", k=1) == [("n", "b")]


def test_blocks_arriving_before_note_created_join_the_workspace_later(app):
    app.handle(Event("e1", BlocksChanged("n", (ChangedBlock("b", "bloom filters"),))))
    assert hits(app, "bloom filters", k=1) == []
    app.handle(Event("e2", NoteCreated("n", WS, "Filters")))
    assert hits(app, "bloom filters", k=1) == [("n", "b")]


def test_search_results_are_cached_and_invalidated_by_changes(app, feed):
    first = app.semantic_search(WS, "sourdough", k=2)
    second = app.semantic_search(WS, "sourdough", k=2)
    assert not first.cached and second.cached and second.hits == first.hits
    feed.note("rye", "Rye", "rye sourdough loaf")
    third = app.semantic_search(WS, "sourdough", k=2)
    assert not third.cached
    assert ("rye", "b0") in [(h.note_id, h.block_id) for h in third.hits]


def test_unchanged_text_is_not_embedded_twice():
    embedder = CountingEmbedder()
    app = make_app(embedder)
    feed = Feed(app)
    feed.note("a", "A", "same text")
    feed.note("b", "B", "same text")
    app.semantic_search(WS, "same text")
    app.semantic_search(WS, "same text", k=3)
    assert embedder.texts == 1


def test_measured_recall_compares_against_an_exact_scan(app, feed):
    outcome = app.semantic_search(
        WS, "leader election", k=3, index=IndexKind.HNSW, measure_recall=True
    )
    assert outcome.recall_at_k == 1.0
    assert outcome.latency_ms >= 0


def test_small_workspaces_default_to_the_exact_index(app, feed):
    assert app.semantic_search(WS, "raft").index == IndexKind.BRUTE_FORCE


@pytest.mark.parametrize(("query", "k"), [("", 5), ("raft", -1), ("raft", 10_000)])
def test_invalid_search_requests_are_rejected(app, query, k):
    with pytest.raises(InvalidArgument):
        app.semantic_search(WS, query, k=k)


def test_discover_reports_signals_and_suggests_unlinked_close_notes(feed):
    app = feed.app
    feed.send(LinksChanged("raft", ("bread",)))
    related = {r.note_id: r for r in make_discovery(app, threshold=0.2)}
    assert related["paxos"].hops == -1 and related["paxos"].suggest_link
    assert related["paxos"].shared_tags == 1 and related["paxos"].tag_jaccard == 1.0
    assert related["bread"].hops == 1 and not related["bread"].suggest_link
    assert related["paxos"].cosine > related["bread"].cosine


def make_discovery(app, threshold):
    app.settings = Settings(suggest_threshold=threshold)
    return app.discover("raft", k=5)


def test_discover_on_an_unknown_note_is_not_found(app):
    with pytest.raises(NotFound):
        app.discover("missing")


def test_clusters_group_notes_by_topic_with_distinctive_labels():
    app = make_app()
    feed = Feed(app)
    for i in range(6):
        feed.note(f"db{i}", "Storage", f"btree index pages and write ahead log {i}")
        feed.note(f"cook{i}", "Kitchen", f"braise onions garlic stock {i}")
    clusters = app.list_clusters(WS)
    groups = sorted(tuple(sorted({n[:2] for n in c.note_ids})) for c in clusters)
    assert groups == [("co",), ("db",)]
    terms = {t for c in clusters for t in c.top_terms[:3]}
    assert terms & {"btree", "index", "pages", "write", "ahead", "log", "storage"}
    assert terms & {"braise", "onions", "garlic", "stock", "kitchen"}
    assert app.list_clusters(WS) == clusters  # cached


def test_benchmark_runs_every_algorithm_on_workspace_vectors():
    app = make_app()
    feed = Feed(app)
    rng = np.random.default_rng(0)
    words = ["raft", "paxos", "btree", "bread", "garlic", "onion", "index", "log"]
    for i in range(60):
        feed.note(f"n{i:02}", "t", " ".join(rng.choice(words, size=6)))
    results = app.benchmark_clustering(
        WS, ["kmeans_plus_plus", "e_means", "gmm_em"], k=4, runs=2, seed=1, include_references=False
    )
    assert [r.algorithm for r in results] == ["kmeans_plus_plus", "e_means", "gmm_em"]
    assert all(r.implementation == "scratch" and r.train_points == 48 for r in results)


def test_workspace_vectors_stay_consistent_under_random_edits(seed):
    rng = np.random.default_rng(seed)
    vectors = WorkspaceVectors(8, IndexSettings(hnsw_m=4, hnsw_ef_construction=16, seed=seed))
    oracle: dict[BlockKey, np.ndarray] = {}
    keys = [BlockKey(f"n{i % 7}", f"b{i}") for i in range(40)]
    for step in range(300):
        key = keys[int(rng.integers(len(keys)))]
        if rng.random() < 0.7:
            vector = rng.normal(size=8).astype(np.float32)
            vectors.upsert(key, vector)
            oracle[key] = vector / np.linalg.norm(vector)
        else:
            vectors.remove([key])
            oracle.pop(key, None)
        if step == 50:  # build the approximate indexes mid-way
            query = rng.normal(size=8)
            vectors.search(query, 3, IndexKind.HNSW)
            vectors.search(query, 3, IndexKind.IVF)
        vectors.check_invariants()
        query = rng.normal(size=8).astype(np.float32)
        found, _ = vectors.search(query, 5, IndexKind.BRUTE_FORCE)
        scores = {k: float(v @ (query / np.linalg.norm(query))) for k, v in oracle.items()}
        expected = sorted(scores, key=lambda k: (-scores[k], k))[:5]
        assert {k for k, _ in found} == set(expected) or np.allclose(
            sorted(s for _, s in found), sorted(scores[k] for k in expected), atol=1e-6
        )
    assert set(vectors.keys()) == set(oracle)
