"""In-process broker with the Redis backend's semantics, for tests.

Topics fan out to subscriptions; a nack schedules a retry after the
subscription's backoff and holds back later messages with the same
ordering key; after the maximum attempts a message is dead-lettered (and
forwarded to the dead-letter topic, with Pub/Sub's attributes). `deliver`
runs one synchronous pass, which makes consumer tests deterministic;
`subscribe` loops passes on a thread like a real subscriber.

Invariants (`check_invariants`): every subscription is listed by its topic;
queued messages have attempts left; dead letters used all of theirs.
"""

from __future__ import annotations

import logging
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass, field, replace

from .broker import Handler
from .envelope import (
    ATTR_DEAD_LETTER_DELIVERY_COUNT,
    ATTR_DEAD_LETTER_SUBSCRIPTION,
    Envelope,
    Message,
    UnknownSubscription,
    UnknownTopic,
)
from .topology import SubscriptionSpec, Topology

__all__ = ["MemoryBroker"]

log = logging.getLogger(__name__)


@dataclass
class _Queued:
    envelope: Envelope
    publish_time: float
    attempts: int = 0
    not_before: float = 0.0


@dataclass
class _MemorySub:
    spec: SubscriptionSpec
    queue: list[_Queued] = field(default_factory=list)
    dead: list[Message] = field(default_factory=list)
    delivering: threading.Lock = field(default_factory=threading.Lock)


class MemoryBroker:
    """See the module doc."""

    def __init__(self, clock: Callable[[], float] = time.time) -> None:
        self._clock = clock
        self._changed = threading.Condition()
        self._topics: dict[str, list[str]] = {}
        self._subs: dict[str, _MemorySub] = {}

    def provision(self, topology: Topology) -> None:
        """Create the topology's topics and subscriptions, skipping existing ones."""
        topology.validate()
        with self._changed:
            for topic in topology.topics:
                self._topics.setdefault(topic, [])
            for spec in topology.subscriptions:
                if spec.name not in self._subs:
                    self._topics[spec.topic].append(spec.name)
                    self._subs[spec.name] = _MemorySub(spec)

    def publish(self, topic: str, envelope: Envelope) -> None:
        """See `Broker.publish`."""
        envelope.validate()
        with self._changed:
            if topic not in self._topics:
                raise UnknownTopic(topic)
            self._enqueue(topic, envelope)

    def _enqueue(self, topic: str, envelope: Envelope) -> None:
        now = self._clock()
        for name in self._topics[topic]:
            self._subs[name].queue.append(_Queued(replace(envelope), now))
        self._changed.notify_all()

    def _sub(self, name: str) -> _MemorySub:
        with self._changed:
            sub = self._subs.get(name)
        if sub is None:
            raise UnknownSubscription(name)
        return sub

    def deliver(self, subscription: str, handler: Handler) -> int:
        """Run one pass over the subscription in publish order; return how many were acked."""
        sub = self._sub(subscription)
        spec = sub.spec
        with sub.delivering:
            with self._changed:
                batch = list(sub.queue)
            blocked: set[str] = set()
            done: set[int] = set()
            acked = 0
            for item in batch:
                key = item.envelope.ordering_key
                if key and key in blocked:
                    continue
                if item.not_before > self._clock():
                    if key:
                        blocked.add(key)
                    continue
                item.attempts += 1
                message = Message(item.envelope, item.publish_time, item.attempts)
                try:
                    handler(message)
                except Exception:
                    log.warning(
                        "event handler failed; nacking",
                        extra={"subscription": subscription, "event_id": item.envelope.id},
                        exc_info=True,
                    )
                else:
                    done.add(id(item))
                    acked += 1
                    continue
                if spec.dead_letter_topic and item.attempts >= spec.max_delivery_attempts:
                    done.add(id(item))
                    self._dead_letter(sub, message)
                    continue
                item.not_before = self._clock() + spec.backoff(item.attempts)
                if key:
                    blocked.add(key)
            with self._changed:
                sub.queue = [q for q in sub.queue if id(q) not in done]
            return acked

    def _dead_letter(self, sub: _MemorySub, message: Message) -> None:
        attrs = dict(message.envelope.attributes)
        attrs[ATTR_DEAD_LETTER_SUBSCRIPTION] = sub.spec.name
        attrs[ATTR_DEAD_LETTER_DELIVERY_COUNT] = str(message.delivery_attempt)
        with self._changed:
            sub.dead.append(message)
            if sub.spec.dead_letter_topic in self._topics:
                self._enqueue(
                    sub.spec.dead_letter_topic, replace(message.envelope, attributes=attrs)
                )

    def next_due(self, subscription: str) -> float | None:
        """When the earliest queued message may be delivered, or None if none is queued."""
        sub = self._sub(subscription)
        with self._changed:
            return min((q.not_before for q in sub.queue), default=None)

    def subscribe(self, subscription: str, handler: Handler) -> _MemorySubscription:
        """See `Broker.subscribe`."""
        self._sub(subscription)
        return _MemorySubscription(self, subscription, handler)

    def pending(self, subscription: str) -> int:
        """How many messages wait in the subscription."""
        sub = self._sub(subscription)
        with self._changed:
            return len(sub.queue)

    def dead_letters(self, subscription: str) -> list[Message]:
        """The messages the subscription gave up on."""
        sub = self._sub(subscription)
        with self._changed:
            return list(sub.dead)

    def close(self) -> None:
        """Nothing to release."""

    def check_invariants(self) -> None:
        """Raise AssertionError if an invariant in the module doc is broken."""
        with self._changed:
            for name, sub in self._subs.items():
                if name not in self._topics.get(sub.spec.topic, []):
                    raise AssertionError(f"{name} is not listed by {sub.spec.topic}")
                limit = sub.spec.max_delivery_attempts if sub.spec.dead_letter_topic else None
                for q in sub.queue:
                    if limit is not None and q.attempts >= limit:
                        raise AssertionError(f"{name} queues {q.envelope.id} after {q.attempts}")
                for m in sub.dead:
                    if m.delivery_attempt != sub.spec.max_delivery_attempts:
                        raise AssertionError(f"{name} dead-lettered {m.envelope.id} early")


class _MemorySubscription:
    def __init__(self, broker: MemoryBroker, name: str, handler: Handler) -> None:
        self._broker, self._name, self._handler = broker, name, handler
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._run, daemon=True, name=f"events-{name}")
        self._thread.start()

    def _run(self) -> None:
        broker = self._broker
        while not self._stop.is_set():
            broker.deliver(self._name, self._handler)
            due = broker.next_due(self._name)
            with broker._changed:
                if self._stop.is_set():
                    return
                wait = 0.05 if due is None else max(0.001, min(0.05, due - broker._clock()))
                broker._changed.wait(wait)

    def stop(self, timeout: float) -> None:
        """See `Subscription.stop`."""
        self._stop.set()
        with self._broker._changed:
            self._broker._changed.notify_all()
        self._thread.join(timeout)
