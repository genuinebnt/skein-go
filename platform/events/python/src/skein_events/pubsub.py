"""Google Cloud Pub/Sub backend (and its emulator, via PUBSUB_EMULATOR_HOST,
which the client library reads). Topics and subscriptions are created by
infrastructure (Terraform, or deploy/compose/pubsub-init.sh), so
`provision` does nothing. Needs the ``pubsub`` extra.

Publishing enables message ordering and waits for the broker's
acknowledgement; after a failure the client pauses the ordering key, so it
is resumed for the caller's retry. Subscribing uses streaming pull with
flow control; a message that is not an envelope is nacked toward the
dead-letter topic.
"""

from __future__ import annotations

import contextlib
import logging
from typing import Any

from .broker import Handler
from .envelope import (
    Envelope,
    InvalidEnvelope,
    Message,
    UnknownSubscription,
    UnknownTopic,
    from_attributes,
    to_attributes,
)
from .topology import Topology

__all__ = ["PubSubBroker"]

log = logging.getLogger(__name__)


class PubSubBroker:
    """See the module doc."""

    def __init__(
        self, project: str, *, max_messages: int = 64, publish_timeout: float = 30.0
    ) -> None:
        try:
            from google.api_core import exceptions
            from google.cloud import pubsub_v1
        except ImportError as error:  # pragma: no cover - depends on the environment
            raise RuntimeError(
                "install the 'pubsub' extra: pip install 'skein-events[pubsub]'"
            ) from error
        self._pubsub, self._exceptions = pubsub_v1, exceptions
        self._project = project
        self._publisher = pubsub_v1.PublisherClient(
            publisher_options=pubsub_v1.types.PublisherOptions(enable_message_ordering=True)
        )
        self._subscriber = pubsub_v1.SubscriberClient()
        self._max_messages = max_messages
        self._timeout = publish_timeout

    def provision(self, topology: Topology) -> None:
        """Pub/Sub's topology belongs to infrastructure."""

    def publish(self, topic: str, envelope: Envelope) -> None:
        """See `Broker.publish`."""
        envelope.validate()
        path = self._publisher.topic_path(self._project, topic)
        try:
            self._publisher.publish(
                path,
                envelope.payload,
                ordering_key=envelope.ordering_key,
                **to_attributes(envelope),
            ).result(timeout=self._timeout)
        except self._exceptions.NotFound as error:
            self._resume(path, envelope)
            raise UnknownTopic(topic) from error
        except Exception:
            self._resume(path, envelope)
            raise

    def _resume(self, path: str, envelope: Envelope) -> None:
        if envelope.ordering_key:
            self._publisher.resume_publish(path, envelope.ordering_key)

    def subscribe(self, subscription: str, handler: Handler) -> _PubSubSubscription:
        """See `Broker.subscribe`. Raises `UnknownSubscription` at once for a missing name."""
        path = self._subscriber.subscription_path(self._project, subscription)
        try:
            self._subscriber.get_subscription(subscription=path)
        except self._exceptions.NotFound as error:
            raise UnknownSubscription(subscription) from error

        def on_message(message: Any) -> None:
            try:
                envelope = from_attributes(
                    dict(message.attributes), message.ordering_key, message.data
                )
            except InvalidEnvelope as error:
                log.error(
                    "malformed event; nacking toward the dead-letter topic",
                    extra={"subscription": subscription, "error": str(error)},
                )
                message.nack()
                return
            delivered = Message(
                envelope, message.publish_time.timestamp(), message.delivery_attempt or 0
            )
            try:
                handler(delivered)
            except Exception:
                log.warning(
                    "event handler failed; nacking",
                    extra={"subscription": subscription, "event_id": envelope.id},
                    exc_info=True,
                )
                message.nack()
                return
            message.ack()

        flow = self._pubsub.types.FlowControl(max_messages=self._max_messages)
        # Without await_callbacks_on_shutdown, result() returns after cancel()
        # while callbacks still run, and a graceful drain would not wait.
        future = self._subscriber.subscribe(
            path, callback=on_message, flow_control=flow, await_callbacks_on_shutdown=True
        )
        return _PubSubSubscription(future)

    def close(self) -> None:
        """Flush publishes and close both clients."""
        with contextlib.suppress(Exception):
            self._publisher.stop()
        self._subscriber.close()


class _PubSubSubscription:
    def __init__(self, future: Any) -> None:
        self._future = future

    def stop(self, timeout: float) -> None:
        """Cancel the stream and wait for in-flight callbacks."""
        self._future.cancel()
        # Cancellation surfaces as an exception from result(); expected here.
        with contextlib.suppress(Exception):
            self._future.result(timeout=timeout)
