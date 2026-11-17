"""Backend selection from the environment, the same variables as Go's
``events.LoadConfig`` (docs/local-first.md):

| Variable | Default | Meaning |
|---|---|---|
| ``EVENTS_BACKEND`` | ``pubsub`` | ``pubsub``, ``redis`` or ``memory`` |
| ``PUBSUB_PROJECT`` | | Pub/Sub project (``pubsub``), unless the service passes its own |
| ``EVENTS_REDIS_URL`` | the service's own Redis URL, then ``REDIS_URL`` | Redis server (``redis``) |
| ``EVENTS_REDIS_DB`` | ``0`` | database holding the streams, whatever the URL names |

Python services pass the values of their own prefixed variables
(``SKEIN_ML_PUBSUB_PROJECT``, ``SKEIN_ML_REDIS_URL``), which take precedence
over the generic ones, so existing deployments keep working unchanged;
``EVENTS_REDIS_URL`` still overrides the service's Redis URL, because that
one names the service's cache.
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from dataclasses import dataclass

from .broker import Broker
from .topology import Topology, default_topology

__all__ = ["BACKENDS", "EventsConfig", "open_broker"]

BACKENDS = ("pubsub", "redis", "memory")


@dataclass(frozen=True)
class EventsConfig:
    """Resolved backend settings."""

    backend: str = "pubsub"
    pubsub_project: str = ""
    redis_url: str = ""
    redis_db: int = 0

    @classmethod
    def from_env(
        cls,
        environ: Mapping[str, str] | None = None,
        *,
        pubsub_project: str = "",
        redis_url: str = "",
    ) -> EventsConfig:
        """Read the variables in the module doc; raise ValueError naming a bad one."""
        env = os.environ if environ is None else environ

        def get(name: str, default: str = "") -> str:
            return env.get(name, "").strip() or default

        backend = get("EVENTS_BACKEND", "pubsub").lower()
        if backend not in BACKENDS:
            raise ValueError(f"EVENTS_BACKEND must be one of {BACKENDS}, not {backend!r}")
        redis_url = get("EVENTS_REDIS_URL", redis_url.strip() or get("REDIS_URL"))
        if backend == "redis" and not redis_url:
            raise ValueError("EVENTS_BACKEND=redis needs EVENTS_REDIS_URL or REDIS_URL")
        raw_db = get("EVENTS_REDIS_DB", "0")
        if not raw_db.isdigit() or not 0 <= int(raw_db) <= 15:
            raise ValueError(f"EVENTS_REDIS_DB={raw_db!r} is not a database number")
        return cls(
            backend=backend,
            pubsub_project=pubsub_project.strip() or get("PUBSUB_PROJECT"),
            redis_url=redis_url,
            redis_db=int(raw_db),
        )

    @property
    def enabled(self) -> bool:
        """False only for Pub/Sub without a project: the service runs without events."""
        return self.backend != "pubsub" or bool(self.pubsub_project)


def open_broker(config: EventsConfig, topology: Topology | None = None) -> Broker:
    """Connect the selected backend and provision it (Redis consumer groups at startup)."""
    if config.backend == "pubsub":
        if not config.pubsub_project:
            raise ValueError("EVENTS_BACKEND=pubsub needs PUBSUB_PROJECT")
        from .pubsub import PubSubBroker

        return PubSubBroker(config.pubsub_project)
    broker: Broker
    if config.backend == "redis":
        from .redis_backend import RedisBroker

        broker = RedisBroker.from_url(config.redis_url, db=config.redis_db)
    else:
        from .memory import MemoryBroker

        broker = MemoryBroker()
    broker.provision(topology or default_topology())
    return broker
