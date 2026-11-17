"""Redis behaviour beyond the shared contract, on fakeredis (in process)."""

from __future__ import annotations

import threading
import time

import fakeredis
import pytest

from skein_events import (
    ATTR_DEAD_LETTER_DELIVERY_COUNT,
    ATTR_DEAD_LETTER_SUBSCRIPTION,
    Envelope,
    InvalidEnvelope,
    Message,
    TopologyBuilder,
    UnknownTopic,
    new_event_id,
)
from skein_events.redis_backend import (
    PARTITIONS,
    RedisBroker,
    _attempts_key,
    _lease_key,
    partition_for,
    stream_key,
)


def topology(attempts=10, backoff=0.05, deadline=10.0):
    return (
        TopologyBuilder()
        .policy(attempts, backoff, 4 * backoff, deadline)
        .topic("t")
        .subscription("s", "t")
        .plain_subscription("dl", "t.dead-letter")
        .build()
    )


@pytest.fixture
def client():
    return fakeredis.FakeRedis(server=fakeredis.FakeServer())


def open_broker(client, topo, name="c"):
    broker = RedisBroker(client, consumer_name=name, poll_interval=0.05)
    broker.provision(topo)
    return broker


def env(key="", payload=b"x"):
    return Envelope(id=new_event_id(), source="documents", payload=payload, ordering_key=key)


def wait_for(what, cond, timeout=20.0):
    deadline = time.monotonic() + timeout
    while not cond():
        assert time.monotonic() < deadline, f"timed out waiting for {what}"
        time.sleep(0.005)


def test_holds_back_later_messages_with_the_same_key_until_the_nacked_one_is_acked(client):
    broker = open_broker(client, topology(backoff=0.1))
    other = next(
        f"k{i}" for i in range(1000) if partition_for(f"k{i}", "") == partition_for("a", "")
    )
    for key, payload in [("a", b"a1"), (other, b"b1"), ("a", b"a2"), (other, b"b2")]:
        broker.publish("t", env(key, payload))
    seen: list[bytes] = []
    failed = []

    def handler(m: Message) -> None:
        seen.append(m.envelope.payload)
        if m.envelope.payload == b"a1" and not failed:
            failed.append(1)
            raise RuntimeError("transient")

    sub = broker.subscribe("s", handler)
    wait_for("five deliveries", lambda: len(seen) == 5)
    sub.stop(5)
    assert seen == [b"a1", b"b1", b"b2", b"a1", b"a2"]
    broker.check_invariants("s")


def test_counts_attempts_and_dead_letters_with_pubsub_attributes(client):
    broker = open_broker(client, topology(attempts=3, backoff=0.005))
    poison = env("n1", b"poison")
    broker.publish("t", poison)
    attempts: list[int] = []

    def fail(m: Message) -> None:
        attempts.append(m.delivery_attempt)
        raise RuntimeError("always fails")

    failing = broker.subscribe("s", fail)
    dead: list[Message] = []
    dl = broker.subscribe("dl", dead.append)
    wait_for("the dead letter", lambda: bool(dead))
    failing.stop(5)
    dl.stop(5)
    assert attempts == [1, 2, 3]
    attrs = dead[0].envelope.attributes
    assert dead[0].envelope.id == poison.id
    assert (
        attrs[ATTR_DEAD_LETTER_SUBSCRIPTION] == "s"
        and attrs[ATTR_DEAD_LETTER_DELIVERY_COUNT] == "3"
    )
    broker.check_invariants("s")


def test_waits_the_backoff_before_redelivering(client):
    broker = open_broker(client, topology(backoff=0.3))
    broker.publish("t", env())
    at: list[float] = []

    def handler(_: Message) -> None:
        at.append(time.monotonic())
        if len(at) == 1:
            raise RuntimeError("transient")

    sub = broker.subscribe("s", handler)
    wait_for("redelivery", lambda: len(at) == 2)
    sub.stop(5)
    assert at[1] - at[0] >= 0.3


def test_redelivers_a_crashed_consumers_message_after_the_visibility_timeout(client):
    deadline = 1.0
    broker = open_broker(client, topology(backoff=0.01, deadline=deadline), "survivor")
    sent = env("n1", b"in flight")
    broker.publish("t", sent)
    p = partition_for("n1", sent.id)
    # The crashed consumer leased the partition, read the entry, counted
    # the attempt, and vanished.
    client.set(_lease_key("s", p), "crashed#1", px=int(deadline * 1000))
    entry_id = client.xreadgroup("s", "crashed", {stream_key("t", p): ">"}, count=1)[0][1][0][0]
    client.hincrby(_attempts_key("s", p), entry_id, 1)
    crashed_at = time.monotonic()

    got: list[Message] = []
    sub = broker.subscribe("s", got.append)
    wait_for("redelivery", lambda: bool(got))
    sub.stop(5)
    assert time.monotonic() - crashed_at >= deadline * 0.9, "delivered while the lease was live"
    assert got[0].envelope.id == sent.id and got[0].delivery_attempt == 2
    broker.check_invariants("s")


def test_two_consumers_share_partitions_and_keep_per_key_order(client):
    topo = topology(attempts=1000, backoff=0.001, deadline=0.3)
    first, second = open_broker(client, topo, "first"), open_broker(client, topo, "second")
    lock = threading.Lock()
    acked: dict[str, list[str]] = {}
    by: dict[str, int] = {"first": 0, "second": 0}
    counter = [0]

    def handler(name):
        def handle(m: Message) -> None:
            with lock:
                counter[0] += 1
                if counter[0] % 4 == 0:
                    raise RuntimeError("periodic failure")
                acked.setdefault(m.envelope.ordering_key, []).append(m.envelope.id)
                by[name] += 1

        return handle

    published: dict[str, list[str]] = {}

    def publish(n, offset):
        for i in range(n):
            e = env(f"note-{(i + offset) % 12}")
            published.setdefault(e.ordering_key, []).append(e.id)
            first.publish("t", e)

    total = lambda: sum(len(v) for v in acked.values())  # noqa: E731
    a = first.subscribe("s", handler("first"))
    publish(100, 0)
    wait_for("progress", lambda: total() > 10)
    b = second.subscribe("s", handler("second"))
    publish(100, 5)
    wait_for("all acked", lambda: total() == 200, timeout=60)
    a.stop(5)
    b.stop(5)
    assert acked == published
    assert by["second"] > 0, "the second consumer never got a partition"
    first.check_invariants("s")


def test_dead_letters_a_malformed_entry_without_calling_the_handler(client):
    broker = open_broker(client, topology(attempts=2, backoff=0.001))
    client.xadd(stream_key("t", 0), {"payload": b"no id"})
    called = []
    sub = broker.subscribe("s", called.append)
    wait_for(
        "the dead letter",
        lambda: sum(client.xlen(stream_key("t.dead-letter", p)) for p in range(PARTITIONS)) == 1,
    )
    sub.stop(5)
    assert not called


def test_publish_rejects_invalid_envelopes_and_unknown_topics(client):
    broker = open_broker(client, topology())
    with pytest.raises(InvalidEnvelope):
        broker.publish("t", Envelope(id="", source="x"))
    with pytest.raises(UnknownTopic):
        broker.publish("nope", env())


def test_provision_is_idempotent(client):
    broker = open_broker(client, topology())
    broker.provision(topology())
    assert len(client.xinfo_groups(stream_key("t", 3))) == 1
