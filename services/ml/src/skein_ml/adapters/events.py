"""`EventSource` for ``documents.events`` over any `skein_events` backend.

The backend is the one ``EVENTS_BACKEND`` selects (Pub/Sub or its emulator,
Redis Streams, or in-process; ADR-0022), so ml consumes the same events
with or without Google tooling. Each message's payload and its Pub/Sub-form
attributes go through ``decode`` (`consumer.decode_or_skip`); the handler
returning acks, raising nacks, and the backend redelivers with the
subscription's backoff, then dead-letters. With ordering keys (the note id)
a note's events arrive in order. ``stop`` waits for the event in flight,
then closes the broker.
"""

from __future__ import annotations

import logging
from collections.abc import Callable

from skein_events import Broker, Message, Subscription

from ..app.ports import EventHandler

__all__ = ["BrokerEventSource"]

log = logging.getLogger(__name__)


class BrokerEventSource:
    """Implements `EventSource` on a `skein_events.Broker`."""

    def __init__(self, broker: Broker, subscription: str, decode: Callable) -> None:
        self._broker = broker
        self._subscription_name = subscription
        self._decode = decode
        self._subscription: Subscription | None = None

    def start(self, handler: EventHandler) -> None:
        """See `EventSource.start`."""

        def deliver(message: Message) -> None:
            handler(self._decode(message.envelope.payload, message.wire_attributes()))

        self._subscription = self._broker.subscribe(self._subscription_name, deliver)
        log.info("subscribed", extra={"subscription": self._subscription_name})

    def stop(self, timeout: float) -> None:
        """Stop delivering, wait up to ``timeout`` for the event in flight, close the broker."""
        if self._subscription is not None:
            self._subscription.stop(timeout)
        self._broker.close()
