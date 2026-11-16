"""Tokenising and tags, the hashing embedder, ONNX pooling, and discovery signals."""

from __future__ import annotations

import os

import numpy as np
import pytest

from skein_ml.adapters.onnx_embedder import mean_pool
from skein_ml.domain.discovery import (
    NOT_CONNECTED,
    jaccard,
    link_hops,
    related_notes,
    should_suggest_link,
)
from skein_ml.domain.embedding import HashingEmbedder
from skein_ml.domain.text import extract_tags, top_terms


def test_tags_are_hashtags_not_language_names_or_fragments():
    text = "Notes on #Rust and #db/infra, unlike C# or a.html#anchor or ##heading #2x"
    assert extract_tags(text) == frozenset({"rust", "db/infra"})


def test_hashing_embedder_is_deterministic_and_unit_length():
    embedder = HashingEmbedder(64)
    texts = ["the log-structured merge tree", "", "the log-structured merge tree"]
    vectors = embedder.embed(texts)
    assert vectors.dtype == np.float32 and vectors.shape == (3, 64)
    np.testing.assert_array_equal(vectors[0], vectors[2])
    assert np.linalg.norm(vectors[0]) == pytest.approx(1.0)
    assert not vectors[1].any()


def test_hashing_embedder_ranks_lexical_overlap_higher():
    embedder = HashingEmbedder(256)
    query, close, far = embedder.embed(
        [
            "raft leader election and log replication",
            "log replication in raft after a leader election",
            "sourdough bread with a long cold proof",
        ]
    )
    assert query @ close > 0.5 > query @ far


def test_fitted_idf_downweights_common_terms_and_changes_the_model_id():
    corpus = ["the cache", "the index", "the queue", "the planner"]
    plain = HashingEmbedder(128)
    fitted = HashingEmbedder.fit(corpus, 128)
    assert plain.model_id != fitted.model_id
    a, b = fitted.embed(["the cache", "the index"])
    p, q = plain.embed(["the cache", "the index"])
    assert a @ b < p @ q


def test_mean_pool_ignores_padding_tokens():
    hidden = np.array([[[1.0, 0.0], [3.0, 0.0], [100.0, 100.0]]])
    mask = np.array([[1, 1, 0]])
    np.testing.assert_allclose(mean_pool(hidden, mask), [[1.0, 0.0]])


@pytest.mark.onnx
def test_onnx_embedder_produces_384_dimensional_unit_vectors():
    from skein_ml.adapters.onnx_embedder import OnnxSentenceEmbedder

    model_dir = os.environ.get("SKEIN_ONNX_MODEL_DIR")
    if not model_dir:
        pytest.skip("set SKEIN_ONNX_MODEL_DIR to a local sentence-transformer export")
    embedder = OnnxSentenceEmbedder(model_dir)
    vectors = embedder.embed(["a note about databases", "a note about cooking"])
    assert vectors.shape == (2, 384)
    np.testing.assert_allclose(np.linalg.norm(vectors, axis=1), 1.0, rtol=1e-5)


def test_top_terms_prefers_terms_distinctive_to_a_cluster():
    labels = top_terms(
        [
            ["raft raft consensus log", "raft quorum notes"],
            ["bread proof flour notes", "flour hydration"],
        ],
        count=2,
    )
    assert labels[0][0] == "raft"
    assert labels[1][0] == "flour"
    assert "notes" not in labels[0][:1] + labels[1][:1]


def test_link_hops_ignore_direction_and_omit_unreachable_notes():
    outbound = {"a": ["b"], "c": ["b"], "d": ["c"], "x": ["y"]}
    assert link_hops(outbound, "a") == {"a": 0, "b": 1, "c": 2, "d": 3}


def test_suggestion_needs_close_meaning_and_no_direct_link():
    assert should_suggest_link(0.70, 2)
    assert should_suggest_link(0.95, NOT_CONNECTED)
    assert not should_suggest_link(0.95, 1)
    assert not should_suggest_link(0.69, NOT_CONNECTED)
    assert should_suggest_link(0.5, 3, threshold=0.4)


def test_jaccard_of_two_empty_tag_sets_is_zero():
    assert jaccard(frozenset(), frozenset()) == 0.0
    assert jaccard(frozenset({"a", "b"}), frozenset({"b", "c"})) == pytest.approx(1 / 3)


def test_related_notes_report_each_signal_separately_ranked_by_cosine():
    unit = lambda v: np.asarray(v, dtype=np.float32) / np.linalg.norm(v)  # noqa: E731
    embeddings = {
        "t": unit([1, 0, 0]),
        "linked": unit([1, 0.1, 0]),
        "far": unit([1, 0.2, 0]),
        "unrelated": unit([0, 0, 1]),
        "tagged": unit([0.1, 1, 0]),
    }
    tags = {
        "t": frozenset({"db", "rust"}),
        "tagged": frozenset({"db", "rust"}),
        "far": frozenset({"db"}),
    }
    outbound = {"t": ["linked"], "linked": ["mid"], "mid": ["far"]}
    related = {r.note_id: r for r in related_notes("t", embeddings, tags, outbound, k=10)}
    assert list(related) == ["linked", "far", "tagged", "unrelated"]
    assert (related["linked"].hops, related["linked"].suggest_link) == (1, False)
    assert (related["far"].hops, related["far"].suggest_link) == (3, True)
    assert related["far"].shared_tags == 1 and related["far"].tag_jaccard == 0.5
    # Identical tags but distant meaning: a strong tag signal, no suggestion.
    assert related["tagged"].tag_jaccard == 1.0 and not related["tagged"].suggest_link
    assert related["unrelated"].hops == NOT_CONNECTED
