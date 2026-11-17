"""Entry point: ``python -m skein_ml`` or the ``skein-ml`` script.

Wiring only: configuration, logging and telemetry, adapters chosen by
configuration (in-memory unless Postgres or Redis are configured; events
from the backend ``EVENTS_BACKEND`` selects),
the gRPC server with health, and the ``documents.events`` consumer.

Graceful shutdown on SIGTERM or SIGINT:

1. health reports NOT_SERVING and ``/readyz`` 503, so load balancers stop
   sending traffic;
2. the consumer stops pulling and finishes in-flight events;
3. the gRPC server stops accepting RPCs and gives in-flight ones the grace
   period (``SKEIN_ML_SHUTDOWN_GRACE_SECONDS``);
4. telemetry is flushed and connections closed.
"""

from __future__ import annotations

import logging
import signal
import threading
from dataclasses import dataclass, field

from .app.service import MLApp, Settings
from .config import Config

__all__ = ["Runtime", "build", "main"]

log = logging.getLogger("skein_ml")

SERVICE = "ml"
# messaging.system per backend, as Go's platform/events names them.
_MESSAGING_SYSTEMS = {"pubsub": "gcp_pubsub", "redis": "redis", "memory": "memory"}


@dataclass
class Runtime:
    """Everything `main` started, for shutdown (and for tests of the wiring)."""

    app: MLApp
    server: object
    health: object
    port: int
    telemetry: object
    metrics: object
    consumer: object | None = None
    closers: list = field(default_factory=list)
    admin: object | None = None
    serving: threading.Event = field(default_factory=threading.Event)

    def shutdown(self, grace: float) -> None:
        """Run the shutdown sequence in the module doc."""
        self.serving.clear()
        self.health.enter_graceful_shutdown()
        log.info("shutting down", extra={"grace_seconds": grace})
        if self.consumer is not None:
            self.consumer.stop(grace / 2)
        self.server.stop(grace).wait()
        for close in self.closers:
            close()
        if self.admin is not None:
            self.admin.stop()
        self.telemetry.shutdown()
        log.info("stopped")


def _embedder(config: Config):
    if config.embedder == "onnx":
        from .adapters.onnx_embedder import OnnxSentenceEmbedder

        return OnnxSentenceEmbedder(config.onnx_model_dir)
    from .domain.embedding import HashingEmbedder

    return HashingEmbedder(config.hashing_dimension)


def build(config: Config, telemetry=None) -> Runtime:
    """Construct (but do not start) the service from ``config``."""
    from .adapters import memory
    from .adapters.grpc_server import build_server
    from .adapters.observability import ServiceMetrics, setup_telemetry

    telemetry = telemetry or setup_telemetry(SERVICE, config.version, config.env)
    closers = []
    if config.database_url:
        from .adapters.postgres import PostgresStore

        store = PostgresStore(config.database_url)
        notes, embeddings, processed = store.notes, store.embeddings, store.processed
        closers.append(store.close)
    else:
        notes = memory.InMemoryNoteRepository()
        embeddings = memory.InMemoryEmbeddingRepository()
        processed = memory.InMemoryProcessedEvents()
    if config.redis_url:
        from .adapters.redis_cache import RedisCache

        cache = RedisCache.from_url(config.redis_url)
    else:
        cache = memory.InMemoryCache()
    app = MLApp(
        notes,
        embeddings,
        processed,
        cache,
        _embedder(config),
        Settings(
            suggest_threshold=config.suggest_threshold,
            cluster_algorithm=config.cluster_algorithm,
        ),
    )
    service_metrics = ServiceMetrics(telemetry.meter)
    server, health, port = build_server(
        app, config.address, telemetry.tracer, service_metrics, config.workers
    )
    consumer = None
    if config.events.enabled:
        from skein_events import open_broker

        from .adapters.consumer import decode_or_skip
        from .adapters.events import BrokerEventSource

        consumer = BrokerEventSource(
            open_broker(config.events), config.pubsub_subscription, decode_or_skip
        )
    runtime = Runtime(app, server, health, port, telemetry, service_metrics, consumer, closers)
    if config.admin_address:
        from .adapters.admin import AdminServer

        runtime.admin = AdminServer(config.admin_address, runtime.serving.is_set)
    return runtime


def main() -> None:
    """Run until SIGTERM or SIGINT, then shut down gracefully."""
    from .adapters.consumer import observed_handler
    from .adapters.observability import configure_logging

    config = Config.from_env()
    configure_logging(SERVICE, config.version, config.env, config.log_level)
    runtime = build(config)
    stop = threading.Event()
    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, lambda *_: stop.set())
    runtime.server.start()
    if runtime.admin is not None:
        runtime.admin.start()
    runtime.serving.set()
    if runtime.consumer is not None:
        runtime.consumer.start(
            observed_handler(
                runtime.app,
                runtime.telemetry.tracer,
                runtime.metrics,
                messaging_system=_MESSAGING_SYSTEMS[config.events.backend],
            )
        )
    log.info(
        "serving",
        extra={
            "port": runtime.port,
            "embedder": runtime.app.embedder.model_id,
            "consumer": runtime.consumer is not None,
            "events_backend": config.events.backend,
        },
    )
    stop.wait()
    runtime.shutdown(config.shutdown_grace_seconds)


if __name__ == "__main__":
    main()
