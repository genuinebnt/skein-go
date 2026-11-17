"""Envelopes and messages, as in Go's ``platform/events`` (``events.go``).

An `Envelope` is one event in transport form: the encoded ``skein.v1.Event``
as bytes, which this package never decodes, plus the fields a broker or a
consumer needs without decoding (id, source, ordering key, traceparent,
attributes). On Pub/Sub, id, source and traceparent travel as the reserved
attributes ``skein-event-id``, ``skein-source`` and ``traceparent``.
"""

from __future__ import annotations

import os
import time
import uuid
from dataclasses import dataclass, field

__all__ = [
    "ATTR_DEAD_LETTER_DELIVERY_COUNT",
    "ATTR_DEAD_LETTER_SUBSCRIPTION",
    "ATTR_EVENT_ID",
    "ATTR_SOURCE",
    "ATTR_TRACEPARENT",
    "Envelope",
    "EventsError",
    "InvalidEnvelope",
    "Message",
    "UnknownSubscription",
    "UnknownTopic",
    "from_attributes",
    "new_event_id",
    "to_attributes",
]

ATTR_EVENT_ID = "skein-event-id"
ATTR_SOURCE = "skein-source"
ATTR_TRACEPARENT = "traceparent"
ATTR_DEAD_LETTER_SUBSCRIPTION = "CloudPubSubDeadLetterSourceSubscription"
"""Added to a dead-lettered message, named as Pub/Sub names it."""
ATTR_DEAD_LETTER_DELIVERY_COUNT = "CloudPubSubDeadLetterSourceDeliveryCount"


class EventsError(Exception):
    """Base of this package's errors."""


class InvalidEnvelope(EventsError, ValueError):
    """An envelope lacks id or source, or uses a reserved attribute."""


class UnknownTopic(EventsError, LookupError):
    """The topic does not exist."""


class UnknownSubscription(EventsError, LookupError):
    """The subscription does not exist."""


def new_event_id() -> str:
    """A UUIDv7, the event ID format (ADR-0004), time-ordered."""
    if hasattr(uuid, "uuid7"):  # Python 3.14
        return str(uuid.uuid7())
    ms = time.time_ns() // 1_000_000
    raw = bytearray(ms.to_bytes(6, "big") + os.urandom(10))
    raw[6] = (raw[6] & 0x0F) | 0x70
    raw[8] = (raw[8] & 0x3F) | 0x80
    return str(uuid.UUID(bytes=bytes(raw)))


@dataclass(frozen=True)
class Envelope:
    """One event in transport form."""

    id: str
    source: str
    payload: bytes = b""
    ordering_key: str = ""
    traceparent: str = ""
    attributes: dict[str, str] = field(default_factory=dict)

    def validate(self) -> None:
        """Raise `InvalidEnvelope` if the envelope cannot be published."""
        if not self.id or not self.source:
            raise InvalidEnvelope("id and source are required")
        for name in self.attributes:
            if name in (ATTR_EVENT_ID, ATTR_SOURCE, ATTR_TRACEPARENT) or name.lower().startswith(
                "goog"
            ):
                raise InvalidEnvelope(f"attribute {name!r} is reserved")


@dataclass(frozen=True)
class Message:
    """An envelope as delivered to a handler."""

    envelope: Envelope
    publish_time: float
    """Unix seconds, as the broker stamped it."""
    delivery_attempt: int
    """Counts from 1; Pub/Sub reports it only with a dead-letter policy (0 otherwise)."""

    def wire_attributes(self) -> dict[str, str]:
        """The Pub/Sub attribute form, which ml's decoder accepts."""
        return to_attributes(self.envelope)


def to_attributes(e: Envelope) -> dict[str, str]:
    """The Pub/Sub wire form of the envelope's metadata."""
    attrs = dict(e.attributes)
    attrs[ATTR_EVENT_ID] = e.id
    attrs[ATTR_SOURCE] = e.source
    if e.traceparent:
        attrs[ATTR_TRACEPARENT] = e.traceparent
    return attrs


def from_attributes(attrs: dict[str, str], ordering_key: str, payload: bytes) -> Envelope:
    """Rebuild an envelope from the Pub/Sub wire form."""
    rest = {
        k: v for k, v in attrs.items() if k not in (ATTR_EVENT_ID, ATTR_SOURCE, ATTR_TRACEPARENT)
    }
    e = Envelope(
        id=attrs.get(ATTR_EVENT_ID, ""),
        source=attrs.get(ATTR_SOURCE, ""),
        payload=payload,
        ordering_key=ordering_key,
        traceparent=attrs.get(ATTR_TRACEPARENT, ""),
        attributes=rest,
    )
    if not e.id or not e.source:
        raise InvalidEnvelope(f"message lacks {ATTR_EVENT_ID} or {ATTR_SOURCE}")
    return e
