"""Postgres repositories against a real database (``-m integration``).

Needs ``SKEIN_ML_TEST_DATABASE_URL`` pointing at a disposable database
(``make up`` provides one); the test creates and drops its own schema
contents. Skipped by default and when the variable is unset.
"""

from __future__ import annotations

import os

import numpy as np
import pytest

from skein_ml.domain.notes import Note

pytestmark = pytest.mark.integration


@pytest.fixture
def store():
    url = os.environ.get("SKEIN_ML_TEST_DATABASE_URL")
    if not url:
        pytest.skip("SKEIN_ML_TEST_DATABASE_URL is not set")
    from skein_ml.adapters.postgres import PostgresStore

    store = PostgresStore(url)
    yield store
    with store._connection.transaction():
        store._connection.execute(
            "TRUNCATE ml.notes, ml.blocks, ml.block_embeddings, ml.processed_events"
        )
    store.close()


def test_notes_round_trip_with_blocks_and_links(store):
    note = Note("n1", "ws", "Title #db", False, {"b1": "text one", "b2": "text two"}, ("n2",))
    store.notes.put(note)
    assert store.notes.get("n1") == note
    note.blocks.pop("b2")
    note.trashed = True
    store.notes.put(note)
    assert store.notes.in_workspace("ws") == [note]
    store.notes.delete("n1")
    assert store.notes.get("n1") is None


def test_embeddings_are_stored_per_model_and_deleted_per_block(store):
    vector = np.arange(4, dtype=np.float32)
    store.embeddings.put("n1", "m", {"b1": vector, "b2": vector * 2})
    np.testing.assert_array_equal(store.embeddings.get("n1", "m")["b2"], vector * 2)
    assert store.embeddings.get("n1", "other") == {}
    store.embeddings.delete_blocks("n1", ["b1"])
    assert list(store.embeddings.get("n1", "m")) == ["b2"]
    store.embeddings.delete_note("n1")
    assert store.embeddings.get("n1", "m") == {}


def test_processed_events_are_recorded_once(store):
    assert not store.processed.contains("e1")
    store.processed.record("e1")
    store.processed.record("e1")
    assert store.processed.contains("e1")


def test_migrate_runs_again_as_the_service_role(store):
    """The store fixture already migrated; as the ``ml`` role of init.sql,
    which cannot create schemas, a second run must apply nothing and not
    fail."""
    from skein_ml.adapters.postgres import migrate

    assert migrate(store._connection) == []
