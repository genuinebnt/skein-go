"""Builders shared by the service-level tests."""

from __future__ import annotations

from skein_ml.adapters.memory import (
    InMemoryCache,
    InMemoryEmbeddingRepository,
    InMemoryNoteRepository,
    InMemoryProcessedEvents,
)
from skein_ml.app.service import MLApp, Settings
from skein_ml.domain.embedding import HashingEmbedder


class CountingEmbedder:
    """Wraps the hashing embedder, counting texts embedded and optionally failing once."""

    def __init__(self, fail_next: bool = False):
        self.inner = HashingEmbedder(128)
        self.texts = 0
        self.fail_next = fail_next

    @property
    def model_id(self) -> str:
        return self.inner.model_id

    @property
    def dimension(self) -> int:
        return self.inner.dimension

    def embed(self, texts):
        if self.fail_next:
            self.fail_next = False
            raise RuntimeError("embedder unavailable")
        self.texts += len(texts)
        return self.inner.embed(texts)


def make_app(embedder=None, fail_first_embed: bool = False, **settings) -> MLApp:
    """An `MLApp` on in-memory fakes."""
    return MLApp(InMemoryNoteRepository(), InMemoryEmbeddingRepository(),
                 InMemoryProcessedEvents(), InMemoryCache(),
                 embedder or CountingEmbedder(fail_next=fail_first_embed), Settings(**settings))
