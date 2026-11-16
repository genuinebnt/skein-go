"""Google Cloud Pub/Sub `EventSource` for ``documents.events`` (optional).

Streaming pull on one subscription. Each message is decoded and handed to
the handler on the client's callback thread pool; success acks, an
exception nacks (Pub/Sub redelivers, with the subscription's backoff).
Messages are published with the note id as ordering key, so with message
ordering enabled on the subscription a note's events arrive in order.
``max_messages`` bounds the in-flight messages (flow control).

Locally the Pub/Sub emulator is used by setting ``PUBSUB_EMULATOR_HOST``;
the client library reads it. Requires the ``pubsub`` extra. Not exercised
by unit tests (it needs the emulator); `InMemoryEventSource` stands in.
"""

from __future__ import annotations

import contextlib
import logging
from collections.abc import Callable

from ..app.ports import EventHandler

__all__ = ["PubSubEventSource"]

log = logging.getLogger(__name__)


class PubSubEventSource:
    """Streaming-pull subscriber implementing `EventSource`."""

    def __init__(self, project: str, subscription: str, decode: Callable, max_messages: int = 64):
        try:
            from google.cloud import pubsub_v1
        except ImportError as error:  # pragma: no cover - depends on the environment
            raise RuntimeError(
                "install the 'pubsub' extra: pip install 'skein-ml[pubsub]'"
            ) from error
        self._pubsub = pubsub_v1
        self._subscriber = pubsub_v1.SubscriberClient()
        self._path = self._subscriber.subscription_path(project, subscription)
        self._decode = decode
        self._max_messages = max_messages
        self._future = None

    def start(self, handler: EventHandler) -> None:
        """See `EventSource.start`."""

        def on_message(message) -> None:
            try:
                handler(self._decode(message.data, dict(message.attributes)))
            except Exception:
                message.nack()
                return
            message.ack()

        flow = self._pubsub.types.FlowControl(max_messages=self._max_messages)
        self._future = self._subscriber.subscribe(
            self._path, callback=on_message, flow_control=flow
        )
        log.info("subscribed", extra={"subscription": self._path})

    def stop(self, timeout: float) -> None:
        """Cancel the stream and wait for in-flight callbacks, then close the client."""
        if self._future is not None:
            self._future.cancel()
            # Cancellation surfaces as an exception from result(); expected here.
            with contextlib.suppress(Exception):
                self._future.result(timeout=timeout)
        self._subscriber.close()
