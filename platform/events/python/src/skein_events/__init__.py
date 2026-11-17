"""Skein's event transport for Python services (the Python half of
``platform/events``; ADR-0022).

`open_broker(EventsConfig.from_env())` returns a `Broker` for the backend
named by ``EVENTS_BACKEND``: Pub/Sub (``PubSubBroker``), Redis Streams
(``RedisBroker``, wire format in docs/spec/events-redis.md, shared with the
Go services) or in-process (``MemoryBroker``). All three deliver at least
once, redeliver a nacked message after the subscription's backoff,
dead-letter after its maximum attempts, and deliver each ordering key's
messages first in publish order; ``tests/test_contract.py`` runs one suite
against each.

Backends import their client libraries lazily, so the package has no
required dependencies.
"""

from .broker import Broker, Handler, Subscription
from .config import BACKENDS, EventsConfig, open_broker
from .envelope import (
    ATTR_DEAD_LETTER_DELIVERY_COUNT,
    ATTR_DEAD_LETTER_SUBSCRIPTION,
    ATTR_EVENT_ID,
    ATTR_SOURCE,
    ATTR_TRACEPARENT,
    Envelope,
    EventsError,
    InvalidEnvelope,
    Message,
    UnknownSubscription,
    UnknownTopic,
    new_event_id,
)
from .memory import MemoryBroker
from .topology import (
    DEAD_LETTER_SUFFIX,
    SubscriptionSpec,
    Topology,
    TopologyBuilder,
    default_topology,
)

__all__ = [
    "ATTR_DEAD_LETTER_DELIVERY_COUNT",
    "ATTR_DEAD_LETTER_SUBSCRIPTION",
    "ATTR_EVENT_ID",
    "ATTR_SOURCE",
    "ATTR_TRACEPARENT",
    "BACKENDS",
    "DEAD_LETTER_SUFFIX",
    "Broker",
    "Envelope",
    "EventsConfig",
    "EventsError",
    "Handler",
    "InvalidEnvelope",
    "MemoryBroker",
    "Message",
    "Subscription",
    "SubscriptionSpec",
    "Topology",
    "TopologyBuilder",
    "UnknownSubscription",
    "UnknownTopic",
    "default_topology",
    "new_event_id",
    "open_broker",
]
