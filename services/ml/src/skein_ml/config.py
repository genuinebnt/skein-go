"""Service configuration from environment variables.

| Variable | Default | Meaning |
|---|---|---|
| ``SKEIN_ML_ADDRESS`` | ``[::]:50051`` | gRPC listen address |
| ``SKEIN_ML_WORKERS`` | ``8`` | gRPC handler threads |
| ``SKEIN_ML_EMBEDDER`` | ``hashing`` | ``hashing`` or ``onnx`` |
| ``SKEIN_ML_HASHING_DIMENSION`` | ``384`` | buckets of the hashing embedder |
| ``SKEIN_ML_ONNX_MODEL_DIR`` | | local sentence-transformer export (``onnx``) |
| ``SKEIN_ML_DATABASE_URL`` | | Postgres; in-memory repositories when empty |
| ``SKEIN_ML_REDIS_URL`` | | Redis cache; in-memory cache when empty |
| ``SKEIN_ML_PUBSUB_PROJECT`` | | Pub/Sub project; no consumer when empty |
| ``SKEIN_ML_PUBSUB_SUBSCRIPTION`` | ``ml.documents.events`` | subscription |
| ``SKEIN_ML_SUGGEST_THRESHOLD`` | ``0.70`` | cosine for link suggestions |
| ``SKEIN_ML_SHUTDOWN_GRACE_SECONDS`` | ``20`` | drain time on SIGTERM |
| ``SKEIN_ML_LOG_LEVEL`` | ``INFO`` | log level |
| ``SKEIN_ENV``, ``SKEIN_VERSION`` | ``dev``, ``0.0.0`` | attached to logs and telemetry |

Telemetry reads the standard ``OTEL_*`` variables (see
`skein_ml.adapters.observability`). Invalid values fail at startup with a
message naming the variable, rather than at first use.
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from dataclasses import dataclass

__all__ = ["Config", "ConfigError"]


class ConfigError(ValueError):
    """An environment variable has an invalid value."""


@dataclass(frozen=True)
class Config:
    """Resolved settings."""

    address: str = "[::]:50051"
    workers: int = 8
    embedder: str = "hashing"
    hashing_dimension: int = 384
    onnx_model_dir: str = ""
    database_url: str = ""
    redis_url: str = ""
    pubsub_project: str = ""
    pubsub_subscription: str = "ml.documents.events"
    suggest_threshold: float = 0.70
    shutdown_grace_seconds: float = 20.0
    log_level: str = "INFO"
    env: str = "dev"
    version: str = "0.0.0"

    @classmethod
    def from_env(cls, environ: Mapping[str, str] | None = None) -> Config:
        """Read and validate the variables in the module doc."""
        env = os.environ if environ is None else environ

        def text(name: str, default: str) -> str:
            return env.get(name, default).strip() or default

        def number(name: str, default, kind):
            raw = env.get(name, "").strip()
            if not raw:
                return default
            try:
                return kind(raw)
            except ValueError as error:
                raise ConfigError(f"{name}={raw!r} is not a valid {kind.__name__}") from error

        config = cls(
            address=text("SKEIN_ML_ADDRESS", cls.address),
            workers=number("SKEIN_ML_WORKERS", cls.workers, int),
            embedder=text("SKEIN_ML_EMBEDDER", cls.embedder).lower(),
            hashing_dimension=number("SKEIN_ML_HASHING_DIMENSION", cls.hashing_dimension, int),
            onnx_model_dir=text("SKEIN_ML_ONNX_MODEL_DIR", ""),
            database_url=text("SKEIN_ML_DATABASE_URL", ""),
            redis_url=text("SKEIN_ML_REDIS_URL", ""),
            pubsub_project=text("SKEIN_ML_PUBSUB_PROJECT", ""),
            pubsub_subscription=text("SKEIN_ML_PUBSUB_SUBSCRIPTION", cls.pubsub_subscription),
            suggest_threshold=number("SKEIN_ML_SUGGEST_THRESHOLD", cls.suggest_threshold, float),
            shutdown_grace_seconds=number(
                "SKEIN_ML_SHUTDOWN_GRACE_SECONDS", cls.shutdown_grace_seconds, float
            ),
            log_level=text("SKEIN_ML_LOG_LEVEL", cls.log_level).upper(),
            env=text("SKEIN_ENV", cls.env),
            version=text("SKEIN_VERSION", cls.version),
        )
        if config.embedder not in ("hashing", "onnx"):
            raise ConfigError("SKEIN_ML_EMBEDDER must be 'hashing' or 'onnx'")
        if config.embedder == "onnx" and not config.onnx_model_dir:
            raise ConfigError("SKEIN_ML_EMBEDDER=onnx needs SKEIN_ML_ONNX_MODEL_DIR")
        if not 0.0 <= config.suggest_threshold <= 1.0:
            raise ConfigError("SKEIN_ML_SUGGEST_THRESHOLD must be within [0, 1]")
        if config.workers < 1 or config.hashing_dimension < 2:
            raise ConfigError("SKEIN_ML_WORKERS must be >= 1 and SKEIN_ML_HASHING_DIMENSION >= 2")
        return config
