"""Backends for the contract suite.

``memory`` and ``fakeredis`` run by default (no sockets, no services).
``redis-server`` and ``pubsub-emulator`` are marked ``integration`` and need
SKEIN_TEST_REDIS_URL and SKEIN_TEST_PUBSUB_EMULATOR_HOST.
"""

from __future__ import annotations

import os
import time
import uuid
from collections.abc import Callable, Iterator
from dataclasses import dataclass
from pathlib import Path

import pytest

from skein_events import Topology, TopologyBuilder
from skein_events.memory import MemoryBroker
from skein_events.redis_backend import RedisBroker

REPO = Path(__file__).resolve().parents[4]


@dataclass
class Names:
    topic: str
    dead_letters: str
    sub_a: str
    sub_b: str
    dead_letter_sub: str


@dataclass
class Backend:
    """Opens a provisioned broker for a fresh topology."""

    name: str
    open: Callable[[Topology], object]
    min_backoff: float
    max_backoff: float


ATTEMPTS = 5  # Pub/Sub's minimum for a dead-letter policy


def topology_for(backend: Backend) -> tuple[Topology, Names]:
    p = f"pyct{uuid.uuid4().hex[:8]}-"
    names = Names(p + "t", p + "t.dead-letter", p + "a", p + "b", p + "dl")
    topo = (
        TopologyBuilder()
        .policy(ATTEMPTS, backend.min_backoff, backend.max_backoff, 10.0)
        .topic(names.topic)
        .subscription(names.sub_a, names.topic)
        .subscription(names.sub_b, names.topic)
        .plain_subscription(names.dead_letter_sub, names.dead_letters)
        .build()
    )
    return topo, names


def _memory() -> Iterator[Backend]:
    brokers: list[MemoryBroker] = []

    def open_(topo: Topology) -> MemoryBroker:
        broker = MemoryBroker()
        broker.provision(topo)
        brokers.append(broker)
        return broker

    yield Backend("memory", open_, 0.01, 0.02)
    for broker in brokers:
        broker.check_invariants()


def _redis(client) -> Iterator[Backend]:
    opened: list[tuple[RedisBroker, Topology]] = []

    def open_(topo: Topology) -> RedisBroker:
        broker = RedisBroker(client, poll_interval=0.05)
        broker.provision(topo)
        opened.append((broker, topo))
        return broker

    yield Backend("redis", open_, 0.02, 0.05)
    for broker, topo in opened:
        for spec in topo.subscriptions:
            broker.check_invariants(spec.name)


def _pubsub(host: str) -> Iterator[Backend]:
    from google.cloud import pubsub_v1
    from google.protobuf import duration_pb2

    os.environ["PUBSUB_EMULATOR_HOST"] = host
    project = f"skein-pyit-{int(time.time())}"
    publisher, subscriber = pubsub_v1.PublisherClient(), pubsub_v1.SubscriberClient()
    from skein_events.pubsub import PubSubBroker

    def open_(topo: Topology) -> PubSubBroker:
        for topic in topo.topics:
            publisher.create_topic(name=publisher.topic_path(project, topic))
        for s in topo.subscriptions:
            request = {
                "name": subscriber.subscription_path(project, s.name),
                "topic": publisher.topic_path(project, s.topic),
                "enable_message_ordering": True,
                "ack_deadline_seconds": int(s.ack_deadline),
                "retry_policy": {
                    "minimum_backoff": duration_pb2.Duration(seconds=int(s.min_backoff)),
                    "maximum_backoff": duration_pb2.Duration(seconds=int(s.max_backoff)),
                },
            }
            if s.dead_letter_topic:
                request["dead_letter_policy"] = {
                    "dead_letter_topic": publisher.topic_path(project, s.dead_letter_topic),
                    "max_delivery_attempts": s.max_delivery_attempts,
                }
            subscriber.create_subscription(request=request)
        return PubSubBroker(project)

    yield Backend("pubsub", open_, 1.0, 2.0)
    subscriber.close()


@pytest.fixture(
    params=[
        "memory",
        "fakeredis",
        pytest.param("redis-server", marks=pytest.mark.integration),
        pytest.param("pubsub-emulator", marks=pytest.mark.integration),
    ]
)
def backend(request) -> Iterator[Backend]:
    if request.param == "memory":
        yield from _memory()
    elif request.param == "fakeredis":
        import fakeredis

        yield from _redis(fakeredis.FakeRedis(server=fakeredis.FakeServer()))
    elif request.param == "redis-server":
        url = os.environ.get("SKEIN_TEST_REDIS_URL")
        if not url:
            pytest.skip("SKEIN_TEST_REDIS_URL not set")
        import redis

        yield from _redis(redis.Redis.from_url(url))
    else:
        host = os.environ.get("SKEIN_TEST_PUBSUB_EMULATOR_HOST")
        if not host:
            pytest.skip("SKEIN_TEST_PUBSUB_EMULATOR_HOST not set")
        yield from _pubsub(host)
