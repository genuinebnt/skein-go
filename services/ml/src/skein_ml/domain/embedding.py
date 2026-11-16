"""The `Embedder` interface and a deterministic hashing-trick TF-IDF embedder.

`HashingEmbedder` maps text to a fixed-size vector without a vocabulary:
each unigram and bigram is hashed (BLAKE2b, 64 bits) to a bucket and a sign
(the "signed hashing trick" of Weinberger et al., 2009, so collisions cancel
in expectation instead of piling up). Term frequency is sublinear
(``1 + ln tf``) and, optionally, scaled by an inverse document frequency per
bucket learnt with ``fit``. Rows are L2-normalised float32, so cosine is a
dot product; empty text maps to the zero vector.

It needs no model files and no network, gives the same vector for the same
text in any process (Python's ``hash`` is salted, so it is not used), and
captures lexical overlap. It is what the tests and the default service
configuration use; `skein_ml.adapters.onnx_embedder` provides real
sentence embeddings when a model directory is configured.

``model_id`` identifies the embedding space. Vectors from different
``model_id`` values must never be compared, and caches key on it.

Porting notes: BLAKE2b with an 8-byte digest, little-endian; bucket is the
value modulo the dimension and the sign is the top bit.
"""

from __future__ import annotations

import hashlib
import math
from collections import Counter
from collections.abc import Sequence
from typing import Protocol

import numpy as np

from .text import tokens

__all__ = ["Embedder", "HashingEmbedder"]


class Embedder(Protocol):
    """Turns texts into L2-normalised float32 vectors of a fixed dimension."""

    @property
    def model_id(self) -> str:
        """Stable name of the embedding space (model and version)."""
        ...

    @property
    def dimension(self) -> int:
        """Length of every vector."""
        ...

    def embed(self, texts: Sequence[str]) -> np.ndarray:
        """A (len(texts), dimension) float32 array of unit (or zero) rows."""
        ...


class HashingEmbedder:
    """Signed hashing-trick TF-IDF over unigrams and bigrams."""

    def __init__(self, dimension: int = 384, idf: np.ndarray | None = None):
        if dimension < 2:
            raise ValueError("dimension must be at least 2")
        if idf is not None and idf.shape != (dimension,):
            raise ValueError("idf must have one weight per bucket")
        self._dimension = dimension
        self._idf = None if idf is None else idf.astype(np.float32)
        digest = "" if idf is None else "-idf" + hashlib.blake2b(
            self._idf.tobytes(), digest_size=6).hexdigest()
        self._model_id = f"hashing-tfidf-v1-d{dimension}{digest}"

    @property
    def model_id(self) -> str:
        """Includes the dimension and, when fitted, a digest of the IDF table."""
        return self._model_id

    @property
    def dimension(self) -> int:
        """Number of hash buckets."""
        return self._dimension

    @classmethod
    def fit(cls, corpus: Sequence[str], dimension: int = 384) -> HashingEmbedder:
        """An embedder whose IDF weights come from ``corpus`` (smoothed, as scikit-learn)."""
        document_frequency = np.zeros(dimension, dtype=np.float64)
        for text in corpus:
            buckets = {bucket for bucket, _ in map(_bucket_and_sign, _features(text))}
            for bucket in {b % dimension for b in buckets}:
                document_frequency[bucket] += 1
        idf = np.log((1 + len(corpus)) / (1 + document_frequency)) + 1.0
        return cls(dimension, idf)

    def embed(self, texts: Sequence[str]) -> np.ndarray:
        """Embed each text; see the module doc for the weighting."""
        out = np.zeros((len(texts), self._dimension), dtype=np.float32)
        for row, text in enumerate(texts):
            for feature, count in Counter(_features(text)).items():
                bucket, sign = _bucket_and_sign(feature)
                bucket %= self._dimension
                out[row, bucket] += sign * (1.0 + math.log(count))
            if self._idf is not None:
                out[row] *= self._idf
        norms = np.linalg.norm(out, axis=1, keepdims=True)
        return np.divide(out, norms, out=out, where=norms > 0)


def _features(text: str) -> list[str]:
    words = tokens(text)
    return words + [f"{a} {b}" for a, b in zip(words, words[1:], strict=False)]


def _bucket_and_sign(feature: str) -> tuple[int, float]:
    value = int.from_bytes(hashlib.blake2b(feature.encode(), digest_size=8).digest(), "little")
    return value & 0x7FFF_FFFF_FFFF_FFFF, -1.0 if value >> 63 else 1.0
