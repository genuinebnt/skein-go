"""The interfaces every backend implements.

A handler returning normally acks the message; raising nacks it, and the
broker redelivers it after the subscription's backoff, until the maximum
attempts send it to the dead-letter topic. Delivery is at least once.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Protocol

from .envelope import Envelope, Message
from .topology import Topology

__all__ = ["Broker", "Handler", "Subscription"]

Handler = Callable[[Message], None]


class Subscription(Protocol):
    """A running subscriber, delivering on background threads."""

    def stop(self, timeout: float) -> None:
        """Stop delivering; wait up to ``timeout`` seconds for in-flight messages."""
        ...


class Broker(Protocol):
    """Publishes envelopes and starts subscribers."""

    def provision(self, topology: Topology) -> None:
        """Create topics and subscriptions (Redis, memory); a no-op on Pub/Sub."""
        ...

    def publish(self, topic: str, envelope: Envelope) -> None:
        """Send one envelope; returns once the broker has accepted it."""
        ...

    def subscribe(self, subscription: str, handler: Handler) -> Subscription:
        """Start delivering the subscription's messages to ``handler``."""
        ...

    def close(self) -> None:
        """Release connections."""
        ...
