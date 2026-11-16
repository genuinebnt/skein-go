"""Sentence-transformer embeddings with ONNX Runtime, from a local directory.

Expects the layout of an ONNX export of a sentence-transformer such as
``all-MiniLM-L6-v2`` (384 dimensions): ``model.onnx`` (or
``onnx/model.onnx``) and ``tokenizer.json``. Sentence vectors are the
attention-masked mean of the last hidden state, L2-normalised, which is how
those models were trained to be compared.

This adapter never downloads anything: a missing file is an error that
names the path. Provisioning the model is a deployment concern (bake it
into the image or mount it), so tests and CI never touch the network.
Requires the ``onnx`` extra (``onnxruntime``, ``tokenizers``).
"""

from __future__ import annotations

import hashlib
from collections.abc import Sequence
from pathlib import Path

import numpy as np

__all__ = ["OnnxSentenceEmbedder", "mean_pool"]


def mean_pool(hidden: np.ndarray, attention_mask: np.ndarray) -> np.ndarray:
    """Masked mean over tokens, then L2 normalisation; padding never contributes."""
    mask = attention_mask.astype(np.float32)[:, :, None]
    summed = (hidden.astype(np.float32) * mask).sum(axis=1)
    pooled = summed / np.maximum(mask.sum(axis=1), 1e-9)
    norms = np.linalg.norm(pooled, axis=1, keepdims=True)
    return np.divide(pooled, norms, out=np.zeros_like(pooled), where=norms > 0)


class OnnxSentenceEmbedder:
    """An `Embedder` backed by a local ONNX sentence-transformer."""

    def __init__(self, model_dir: str | Path, max_tokens: int = 256, batch_size: int = 32,
                 threads: int | None = None):
        try:
            import onnxruntime
            from tokenizers import Tokenizer
        except ImportError as error:  # pragma: no cover - depends on the environment
            raise RuntimeError("install the 'onnx' extra: pip install 'skein-ml[onnx]'") from error
        directory = Path(model_dir)
        model = next((p for p in (directory / "model.onnx", directory / "onnx" / "model.onnx")
                      if p.is_file()), None)
        tokenizer_file = directory / "tokenizer.json"
        if model is None or not tokenizer_file.is_file():
            raise FileNotFoundError(
                f"{directory} needs model.onnx (or onnx/model.onnx) and tokenizer.json; "
                "models are never downloaded at runtime")
        options = onnxruntime.SessionOptions()
        if threads:
            options.intra_op_num_threads = threads
        self._session = onnxruntime.InferenceSession(str(model), options,
                                                     providers=["CPUExecutionProvider"])
        self._inputs = {i.name for i in self._session.get_inputs()}
        self._tokenizer = Tokenizer.from_file(str(tokenizer_file))
        self._tokenizer.enable_truncation(max_length=max_tokens)
        self._tokenizer.enable_padding()
        self._batch_size = batch_size
        probe = self.embed(["probe"])
        self._dimension = probe.shape[1]
        digest = hashlib.blake2b(model.read_bytes(), digest_size=6).hexdigest()
        self._model_id = f"onnx-{directory.name}-{digest}"

    @property
    def model_id(self) -> str:
        """Directory name plus a digest of the model file."""
        return self._model_id

    @property
    def dimension(self) -> int:
        """Hidden size of the model (384 for MiniLM-L6)."""
        return self._dimension

    def embed(self, texts: Sequence[str]) -> np.ndarray:
        """Embed in batches of ``batch_size``."""
        if not texts:
            return np.zeros((0, getattr(self, "_dimension", 0)), dtype=np.float32)
        parts = []
        for begin in range(0, len(texts), self._batch_size):
            encodings = self._tokenizer.encode_batch(list(texts[begin:begin + self._batch_size]))
            ids = np.array([e.ids for e in encodings], dtype=np.int64)
            mask = np.array([e.attention_mask for e in encodings], dtype=np.int64)
            feed = {"input_ids": ids, "attention_mask": mask}
            if "token_type_ids" in self._inputs:
                feed["token_type_ids"] = np.zeros_like(ids)
            feed = {name: value for name, value in feed.items() if name in self._inputs}
            hidden = self._session.run(None, feed)[0]
            parts.append(mean_pool(hidden, mask))
        return np.concatenate(parts)
