"""The contract every backend shares (Go's platform/events/contract_test.go).

Timing is never asserted, only outcomes within a deadline, because the
brokers' clocks differ. Ordering: each key's messages are first delivered
in publish order; Memory and Redis also hold a key back after a nack
(test_redis.py), the Pub/Sub emulator does not.
"""

from __future__ import annotations

import threading
import time
from collections.abc import Callable

import pytest
from conftest import Backend, topology_for

from skein_events import Envelope, Message, UnknownSubscription, UnknownTopic, new_event_id


def envelope(key: str = "", payload: bytes = b"x", **attrs: str) -> Envelope:
    return Envelope(
        id=new_event_id(), source="documents", payload=payload, ordering_key=key, attributes=attrs
    )


def consume(
    broker, sub: str, handler: Callable[[Message], None], done: Callable[[], bool], timeout=60.0
):
    """Subscribe until done() holds after a handler call, or fail at the deadline."""
    finished = threading.Event()

    def wrapped(message: Message) -> None:
        try:
            handler(message)
        finally:
            if done():
                finished.set()

    subscription = broker.subscribe(sub, wrapped)
    try:
        assert finished.wait(timeout), f"gave up waiting on {sub}"
    finally:
        subscription.stop(10.0)


def test_round_trips_every_envelope_field(backend: Backend):
    topo, n = topology_for(backend)
    broker = backend.open(topo)
    sent = Envelope(
        id=new_event_id(),
        source="documents",
        payload=b"payload \x00 bytes \xff",
        ordering_key="note-1",
        traceparent="00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
        attributes={"kind": "blocks_changed", "empty": ""},
    )
    broker.publish(n.topic, sent)
    got: list[Message] = []
    consume(broker, n.sub_a, got.append, lambda: bool(got))
    assert got[0].envelope == sent
    assert got[0].delivery_attempt == 1
    assert got[0].publish_time > 0


def test_fans_out_to_every_subscription(backend: Backend):
    topo, n = topology_for(backend)
    broker = backend.open(topo)
    sent = envelope()
    broker.publish(n.topic, sent)
    for sub in (n.sub_a, n.sub_b):
        got: list[Message] = []
        consume(broker, sub, got.append, lambda got=got: bool(got))
        assert got[0].envelope.id == sent.id


def test_redelivers_a_nacked_message(backend: Backend):
    topo, n = topology_for(backend)
    broker = backend.open(topo)
    sent = envelope("note-1")
    broker.publish(n.topic, sent)
    attempts: list[int] = []

    def handler(message: Message) -> None:
        attempts.append(message.delivery_attempt)
        if len(attempts) == 1:
            raise RuntimeError("transient")

    consume(broker, n.sub_a, handler, lambda: len(attempts) >= 2)
    assert attempts == [1, 2]


def test_dead_letters_after_max_attempts(backend: Backend):
    topo, n = topology_for(backend)
    broker = backend.open(topo)
    poison = envelope("note-1", b"poison")
    broker.publish(n.topic, poison)

    def fail(_: Message) -> None:
        raise RuntimeError("always fails")

    failing = broker.subscribe(n.sub_a, fail)
    try:
        dead: list[Message] = []
        consume(broker, n.dead_letter_sub, dead.append, lambda: bool(dead))
    finally:
        failing.stop(10.0)
    assert dead[0].envelope.id == poison.id
    assert dead[0].envelope.payload == b"poison"
    assert dead[0].envelope.ordering_key == "note-1"


def test_first_deliveries_keep_per_key_order_across_nacks(backend: Backend):
    topo, n = topology_for(backend)
    broker = backend.open(topo)
    order: dict[str, list[str]] = {}
    for i, key in enumerate(["a", "b", "a", "a", "b", "a", "b"]):
        e = envelope(key, f"{key}{i}".encode())
        order.setdefault(key, []).append(e.id)
        broker.publish(n.topic, e)
    nack_once = order["a"][1]
    first_seen: dict[str, list[str]] = {}
    seen: set[str] = set()
    acked: set[str] = set()
    lock = threading.Lock()

    def handler(message: Message) -> None:
        e = message.envelope
        with lock:
            if e.id not in seen:
                seen.add(e.id)
                first_seen.setdefault(e.ordering_key, []).append(e.id)
            if e.id == nack_once and message.delivery_attempt == 1:
                raise RuntimeError("transient")
            acked.add(e.id)

    consume(broker, n.sub_a, handler, lambda: len(acked) == 7)
    assert first_seen == order


def test_reports_unknown_names(backend: Backend):
    topo, n = topology_for(backend)
    broker = backend.open(topo)
    with pytest.raises(UnknownTopic):
        broker.publish(n.topic + "-missing", envelope())
    with pytest.raises(UnknownSubscription):
        broker.subscribe(n.sub_a + "-missing", lambda _: None)


def test_stop_waits_for_the_message_in_flight(backend: Backend):
    topo, n = topology_for(backend)
    broker = backend.open(topo)
    broker.publish(n.topic, envelope())
    started, finished = threading.Event(), threading.Event()

    def slow(_: Message) -> None:
        started.set()
        time.sleep(0.3)
        finished.set()

    subscription = broker.subscribe(n.sub_a, slow)
    assert started.wait(30)
    subscription.stop(10.0)
    assert finished.is_set()
