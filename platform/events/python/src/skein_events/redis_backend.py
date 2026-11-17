"""Redis Streams backend: docs/spec/events-redis.md, the same keys, fields
and algorithm as Go's ``platform/events/redis.go``, so Go and Python
services share topics.

Summary: each topic is 8 partition streams; an envelope goes to partition
FNV-1a-32(ordering key, or event ID) mod 8, so a key's events keep their
order. Each subscription is a consumer group on every partition. One
consumer at a time owns a partition through a lease that is renewed every
third of the ack deadline; replicas share partitions by heartbeat. A lapsed
lease is the visibility timeout: the next owner claims the partition's
pending entries and redelivers them. A partition worker delivers entries one
at a time in ID order, counts attempts in a hash (before the handler, so a
crash counts), stores a retry time on a nack (1 s doubling to 60 s) and
holds back the key's later entries until then, and after the maximum
attempts copies the entry to the dead-letter topic.

Threads: one balancer per subscription and one worker per owned partition.
redis-py clients are thread-safe (a pool connection per command), so all
share one client; a worker's blocking read holds one connection.

Invariants (`RedisBroker.check_invariants`): attempts and retry times refer
only to pending entries; every retry time has attempts; with a dead-letter
topic no pending entry has used every attempt.
"""

from __future__ import annotations

import logging
import math
import os
import socket
import threading
import time
from collections.abc import Callable
from typing import Any

from .broker import Handler
from .envelope import (
    ATTR_DEAD_LETTER_DELIVERY_COUNT,
    ATTR_DEAD_LETTER_SUBSCRIPTION,
    Envelope,
    InvalidEnvelope,
    Message,
    UnknownSubscription,
    UnknownTopic,
)
from .topology import SubscriptionSpec, Topology

__all__ = ["PARTITIONS", "RedisBroker", "partition_for", "stream_key"]

log = logging.getLogger(__name__)

PARTITIONS = 8
"""Partition streams per topic; part of the wire format."""

_PREFIX = "skein:events:"
_ATTR = "attr:"
_PAGE = 100

# Compare-and-renew and compare-and-delete, so a consumer only touches a
# lease it still holds (the token is unique per acquisition).
_RENEW = """
if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pexpire", KEYS[1], ARGV[2]) end
return 0
"""
_FREE = """
if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) end
return 0
"""


def _fnv1a32(data: bytes) -> int:
    h = 0x811C9DC5
    for byte in data:
        h = ((h ^ byte) * 0x01000193) & 0xFFFFFFFF
    return h


def partition_for(ordering_key: str, event_id: str) -> int:
    """The partition of an envelope (spec section 3)."""
    return _fnv1a32((ordering_key or event_id).encode()) % PARTITIONS


def stream_key(topic: str, partition: int) -> str:
    """The stream holding one partition of a topic."""
    return f"{_PREFIX}stream:{topic}:{partition}"


def _lease_key(sub: str, p: int) -> str:
    return f"{_PREFIX}lease:{sub}:{p}"


def _attempts_key(sub: str, p: int) -> str:
    return f"{_PREFIX}attempts:{sub}:{p}"


def _retry_key(sub: str, p: int) -> str:
    return f"{_PREFIX}retry:{sub}:{p}"


def _consumers_key(sub: str) -> str:
    return f"{_PREFIX}consumers:{sub}"


def _text(value: Any) -> str:
    return value.decode() if isinstance(value, bytes) else str(value)


def encode_fields(e: Envelope) -> list[tuple[str, bytes | str]]:
    """The entry fields of an envelope, in spec order (section 4)."""
    fields: list[tuple[str, bytes | str]] = [("id", e.id), ("source", e.source)]
    if e.ordering_key:
        fields.append(("key", e.ordering_key))
    if e.traceparent:
        fields.append(("traceparent", e.traceparent))
    fields += [(_ATTR + k, e.attributes[k]) for k in sorted(e.attributes)]
    fields.append(("payload", e.payload))
    return fields


def decode_fields(values: dict) -> Envelope:
    """Rebuild an envelope from entry fields; unknown fields are ignored."""
    fields = {_text(k): v for k, v in values.items()}
    attrs = {k[len(_ATTR) :]: _text(v) for k, v in fields.items() if k.startswith(_ATTR)}
    payload = fields.get("payload", b"")
    e = Envelope(
        id=_text(fields.get("id", "")),
        source=_text(fields.get("source", "")),
        payload=payload if isinstance(payload, bytes) else str(payload).encode(),
        ordering_key=_text(fields.get("key", "")),
        traceparent=_text(fields.get("traceparent", "")),
        attributes=attrs,
    )
    if not e.id or not e.source:
        raise InvalidEnvelope("entry lacks id or source")
    return e


def _entry_time(entry_id: str) -> float:
    try:
        return int(entry_id.split("-", 1)[0]) / 1000
    except ValueError:
        return 0.0


class RedisBroker:
    """See the module doc. ``client`` is a ``redis.Redis`` with bytes responses."""

    def __init__(
        self,
        client: Any,
        *,
        consumer_name: str = "",
        max_len: int = 100_000,
        max_outstanding: int = 100,
        poll_interval: float = 1.0,
        clock: Callable[[], float] = time.time,
        owns_client: bool = False,
    ) -> None:
        self._client = client
        self._owns = owns_client
        self._consumer_name = consumer_name or (
            f"{socket.gethostname()}-{os.getpid()}-{os.urandom(4).hex()}"
        )
        self._max_len = max_len
        self._max_outstanding = max_outstanding
        self._poll = poll_interval
        self._clock = clock
        self._lock = threading.Lock()
        self._topology = Topology((), ())
        self._counter = 0
        self._renew = client.register_script(_RENEW)
        self._free = client.register_script(_FREE)

    @classmethod
    def from_url(cls, url: str, db: int = 0, **kwargs: Any) -> RedisBroker:
        """Connect to ``url``, using database ``db`` whatever the URL names (spec section 1)."""
        import redis

        client = redis.Redis.from_url(url, db=db, health_check_interval=30)
        client.ping()
        return cls(client, owns_client=True, **kwargs)

    def _next(self) -> int:
        with self._lock:
            self._counter += 1
            return self._counter

    def provision(self, topology: Topology) -> None:
        """Create every subscription's consumer group on each partition (idempotent)."""
        from redis.exceptions import ResponseError

        topology.validate()
        for spec in topology.subscriptions:
            for p in range(PARTITIONS):
                try:
                    self._client.xgroup_create(
                        stream_key(spec.topic, p), spec.name, id="0", mkstream=True
                    )
                except ResponseError as error:
                    if not str(error).startswith("BUSYGROUP"):
                        raise
        with self._lock:
            self._topology = topology

    def publish(self, topic: str, envelope: Envelope) -> None:
        """See `Broker.publish`."""
        envelope.validate()
        with self._lock:
            known = topic in self._topology.topics
        if not known:
            raise UnknownTopic(topic)
        self._xadd(
            topic, partition_for(envelope.ordering_key, envelope.id), encode_fields(envelope)
        )

    def _xadd(self, topic: str, partition: int, fields: list[tuple[str, Any]]) -> None:
        self._client.xadd(
            stream_key(topic, partition), dict(fields), maxlen=self._max_len, approximate=True
        )

    def _spec(self, name: str) -> SubscriptionSpec:
        with self._lock:
            spec = self._topology.subscription(name)
        if spec is None:
            raise UnknownSubscription(name)
        return spec

    def subscribe(self, subscription: str, handler: Handler) -> RedisSubscription:
        """See `Broker.subscribe`."""
        spec = self._spec(subscription)
        return RedisSubscription(self, spec, handler, f"{self._consumer_name}/{self._next()}")

    def close(self) -> None:
        """Close the client if `from_url` opened it."""
        if self._owns:
            self._client.close()

    def check_invariants(self, subscription: str) -> None:
        """Raise AssertionError if an invariant in the module doc is broken."""
        spec = self._spec(subscription)
        for p in range(PARTITIONS):
            pending = self._client.xpending_range(
                stream_key(spec.topic, p), spec.name, min="-", max="+", count=1 << 20
            )
            in_pel = {_text(e["message_id"]) for e in pending}
            attempts = {
                _text(k): int(v)
                for k, v in self._client.hgetall(_attempts_key(spec.name, p)).items()
            }
            retries = {_text(k) for k in self._client.hkeys(_retry_key(spec.name, p))}
            for entry_id, count in attempts.items():
                if entry_id not in in_pel:
                    raise AssertionError(f"{subscription}/{p} keeps attempts for acked {entry_id}")
                if spec.dead_letter_topic and count >= spec.max_delivery_attempts:
                    raise AssertionError(f"{subscription}/{p} keeps {entry_id} after {count}")
            if not retries <= set(attempts):
                raise AssertionError(f"{subscription}/{p} has retry times without attempts")


class RedisSubscription:
    """One subscriber: balances partition leases and runs a worker per partition."""

    def __init__(
        self, broker: RedisBroker, spec: SubscriptionSpec, handler: Handler, consumer: str
    ) -> None:
        self._b, self._spec, self._handler, self._consumer = broker, spec, handler, consumer
        self._stop = threading.Event()
        self._workers: dict[int, _Worker] = {}
        self._all_workers: list[_Worker] = []
        self._thread = threading.Thread(target=self._run, daemon=True, name=f"events-{spec.name}")
        self._thread.start()

    def _run(self) -> None:
        tick = max(self._spec.ack_deadline / 3, 0.01)
        while not self._stop.is_set():
            try:
                self._balance()
            except Exception as error:
                log.warning(
                    "event partition balancing failed; retrying",
                    extra={"subscription": self._spec.name, "error": str(error)},
                )
            self._stop.wait(tick)

    def _balance(self) -> None:
        b, spec, client = self._b, self._spec, self._b._client
        ttl_ms = int(spec.ack_deadline * 1000)
        now_ms = int(b._clock() * 1000)
        key = _consumers_key(spec.name)
        pipe = client.pipeline(transaction=True)
        pipe.zadd(key, {self._consumer: now_ms})
        pipe.zremrangebyscore(key, "-inf", now_ms - ttl_ms)
        pipe.zcard(key)
        pipe.pexpire(key, 2 * ttl_ms)
        live = max(int(pipe.execute()[2]), 1)
        share = math.ceil(PARTITIONS / live)

        for p, worker in list(self._workers.items()):
            if not b._renew(keys=[_lease_key(spec.name, p)], args=[worker.token, ttl_ms]):
                log.warning(
                    "lost an event partition lease",
                    extra={"subscription": spec.name, "partition": p},
                )
                self._stop_worker(p)
        for p in sorted(self._workers, reverse=True):
            if len(self._workers) <= share:
                break
            self._stop_worker(p)
        offset = partition_for(self._consumer, "")
        for i in range(PARTITIONS):
            if len(self._workers) >= share or self._stop.is_set():
                break
            p = (offset + i) % PARTITIONS
            if p in self._workers:
                continue
            token = f"{self._consumer}#{b._next()}"
            if not client.set(_lease_key(spec.name, p), token, nx=True, px=ttl_ms):
                continue
            try:
                self._claim_all(p)
            except Exception:
                b._free(keys=[_lease_key(spec.name, p)], args=[token])
                raise
            worker = _Worker(self, p, token)
            self._workers[p] = worker
            self._all_workers.append(worker)
            worker.start()

    def _claim_all(self, p: int) -> None:
        # Not JUSTID: redis-py drops the cursor from JUSTID replies, and the
        # loop needs it to page past 1000 pending entries.
        start = "0-0"
        while True:
            reply = self._b._client.xautoclaim(
                stream_key(self._spec.topic, p),
                self._spec.name,
                self._consumer,
                min_idle_time=0,
                start_id=start,
                count=1000,
            )
            start = _text(reply[0])
            if start in ("0-0", ""):
                return

    def _stop_worker(self, p: int) -> None:
        self._workers.pop(p).stop.set()

    def stop(self, timeout: float) -> None:
        """See `Subscription.stop`: workers finish their message and free their leases."""
        self._stop.set()
        self._thread.join(timeout)
        for p in list(self._workers):
            self._stop_worker(p)
        deadline = time.monotonic() + timeout
        for worker in self._all_workers:
            worker.thread.join(max(0.0, deadline - time.monotonic()))
        try:
            self._b._client.zrem(_consumers_key(self._spec.name), self._consumer)
        except Exception:  # best effort: the heartbeat expires anyway
            log.debug("could not leave the consumer set", exc_info=True)


class _Worker:
    """Delivers one owned partition, one message at a time, in entry order."""

    def __init__(self, sub: RedisSubscription, partition: int, token: str) -> None:
        self.sub, self.partition, self.token = sub, partition, token
        self.stop = threading.Event()
        self.thread = threading.Thread(
            target=self._run, daemon=True, name=f"events-{sub._spec.name}-{partition}"
        )

    def start(self) -> None:
        self.thread.start()

    def _halted(self) -> bool:
        return self.stop.is_set() or self.sub._stop.is_set()

    def _run(self) -> None:
        b, spec = self.sub._b, self.sub._spec
        try:
            while not self._halted():
                try:
                    wait = self._pass()
                except Exception as error:
                    log.warning(
                        "event partition pass failed; retrying",
                        extra={
                            "subscription": spec.name,
                            "partition": self.partition,
                            "error": str(error),
                        },
                    )
                    wait = b._poll
                if wait > 0:
                    self.stop.wait(wait)
        finally:
            try:
                b._free(keys=[_lease_key(spec.name, self.partition)], args=[self.token])
            except Exception:  # the lease expires anyway
                log.debug("could not free the lease", exc_info=True)

    def _read(self, start: str, count: int, block_ms: int | None) -> list:
        reply = self.sub._b._client.xreadgroup(
            self.sub._spec.name,
            self.sub._consumer,
            {stream_key(self.sub._spec.topic, self.partition): start},
            count=count,
            block=block_ms,
        )
        if not reply:
            return []
        if isinstance(reply, dict):  # RESP3
            return next(iter(reply.values()))[0] if reply else []
        return reply[0][1]

    def _pass(self) -> float:
        b, spec = self.sub._b, self.sub._spec
        raw = b._client.hgetall(_retry_key(spec.name, self.partition))
        state = _PassState({_text(k): int(v) for k, v in raw.items()}, b._clock() + b._poll)
        start = "0"
        while True:
            entries = self._read(start, _PAGE, None)
            for entry_id, values in entries:
                start = _text(entry_id)
                self._deliver(start, values, state)
                if self._halted():
                    return 0
            if len(entries) < _PAGE:
                break
        room = b._max_outstanding - state.waiting
        if room <= 0:
            return max(state.next_wake - b._clock(), 0.001)
        block_ms = None
        if state.resolved == 0:
            block_ms = max(int((state.next_wake - b._clock()) * 1000), 1)
        for entry_id, values in self._read(">", room, block_ms):
            self._deliver(_text(entry_id), values, state)
            if self._halted():
                break
        return 0

    def _deliver(self, entry_id: str, values: dict | None, state: _PassState) -> None:
        b, spec, client, p = self.sub._b, self.sub._spec, self.sub._b._client, self.partition
        if not values:
            log.warning(
                "pending event was trimmed from its stream before delivery; dropping",
                extra={"subscription": spec.name, "entry_id": entry_id},
            )
            state.resolved += 1
            self._finish(entry_id)
            return
        key = _text(values.get(b"key", values.get("key", b"")))
        if key and key in state.blocked:
            state.waiting += 1
            return
        due = state.retries.get(entry_id)
        if due is not None and due > b._clock() * 1000:
            state.hold(key, due / 1000)
            return
        attempt = int(client.hincrby(_attempts_key(spec.name, p), entry_id, 1))
        acked = False
        try:
            envelope = decode_fields(values)
        except InvalidEnvelope as error:
            log.error(
                "malformed event; nacking toward the dead-letter topic",
                extra={"subscription": spec.name, "entry_id": entry_id, "error": str(error)},
            )
        else:
            try:
                self.sub._handler(Message(envelope, _entry_time(entry_id), attempt))
                acked = True
            except Exception:
                log.warning(
                    "event handler failed; nacking",
                    extra={
                        "subscription": spec.name,
                        "event_id": envelope.id,
                        "delivery_attempt": attempt,
                    },
                    exc_info=True,
                )
        if acked:
            state.resolved += 1
            self._finish(entry_id)
        elif spec.dead_letter_topic and attempt >= spec.max_delivery_attempts:
            self._dead_letter(entry_id, values, attempt)
            state.resolved += 1
            self._finish(entry_id)
        else:
            due_at = b._clock() + spec.backoff(attempt)
            client.hset(_retry_key(spec.name, p), entry_id, int(due_at * 1000))
            state.hold(key, due_at)

    def _finish(self, entry_id: str) -> None:
        spec, p = self.sub._spec, self.partition
        pipe = self.sub._b._client.pipeline(transaction=True)
        pipe.xack(stream_key(spec.topic, p), spec.name, entry_id)
        pipe.hdel(_attempts_key(spec.name, p), entry_id)
        pipe.hdel(_retry_key(spec.name, p), entry_id)
        pipe.execute()

    def _dead_letter(self, entry_id: str, values: dict, attempts: int) -> None:
        spec = self.sub._spec
        fields = sorted(((_text(k), v) for k, v in values.items()), key=lambda kv: kv[0])
        fields += [
            (_ATTR + ATTR_DEAD_LETTER_SUBSCRIPTION, spec.name),
            (_ATTR + ATTR_DEAD_LETTER_DELIVERY_COUNT, str(attempts)),
        ]
        by_name = dict(fields)
        event_id = _text(by_name.get("id", b"")) or entry_id
        log.error(
            "event dead-lettered",
            extra={"subscription": spec.name, "event_id": event_id, "delivery_attempts": attempts},
        )
        key = _text(by_name.get("key", b""))
        self.sub._b._xadd(spec.dead_letter_topic, partition_for(key, event_id), fields)


class _PassState:
    """What one pass knows: retry times (ms), held-back keys, and when to wake."""

    def __init__(self, retries: dict[str, int], next_wake: float) -> None:
        self.retries = retries
        self.blocked: set[str] = set()
        self.next_wake = next_wake
        self.resolved = 0
        self.waiting = 0

    def hold(self, key: str, until: float) -> None:
        self.waiting += 1
        if key:
            self.blocked.add(key)
        self.next_wake = min(self.next_wake, until)
