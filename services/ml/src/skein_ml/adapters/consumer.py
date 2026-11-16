"""Wires an `EventSource` for ``documents.events`` to `MLApp.handle`.

`observed_handler` continues the publisher's trace from ``Event.traceparent``
(a CONSUMER span), uses the event id as the request id in logs, and counts
outcomes (applied, duplicate, failed) by payload kind. A failure re-raises
so the source redelivers; `MLApp.handle` records an event only once applied.

`decode_or_skip` makes a raw-bytes source safe against poison messages:
bytes that cannot parse are logged and acknowledged (returning None)
instead of being redelivered forever.
"""

from __future__ import annotations

import logging
from collections.abc import Callable

from opentelemetry import propagate, trace

from ..app.events import Event
from ..app.service import MLApp
from .events_codec import MalformedEvent, decode_event
from .observability import ServiceMetrics, request_context

__all__ = ["decode_or_skip", "observed_handler"]

log = logging.getLogger(__name__)


def observed_handler(app: MLApp, tracer: trace.Tracer, service_metrics: ServiceMetrics
                     ) -> Callable[[Event | None], None]:
    """An `EventHandler` that applies events through ``app`` with tracing and metrics."""

    def handle(event: Event | None) -> None:
        if event is None:
            return
        kind = str(event.payload.kind)
        parent = propagate.extract({"traceparent": event.traceparent}) if event.traceparent \
            else None
        with request_context(event.id), tracer.start_as_current_span(
                "documents.events process", context=parent, kind=trace.SpanKind.CONSUMER,
                attributes={"messaging.system": "gcp_pubsub", "messaging.message.id": event.id,
                            "skein.event.kind": kind}):
            try:
                applied = app.handle(event)
            except Exception:
                service_metrics.events.add(1, {"kind": kind, "outcome": "failed"})
                log.exception("event failed; it will be redelivered", extra={"kind": kind})
                raise
            outcome = "applied" if applied else "duplicate"
            service_metrics.events.add(1, {"kind": kind, "outcome": outcome})
            log.debug("event handled", extra={"kind": kind, "outcome": outcome})

    return handle


def decode_or_skip(data: bytes, attributes: dict[str, str] | None = None) -> Event | None:
    """Decode an envelope, or log and return None for one that can never parse."""
    try:
        return decode_event(data, attributes)
    except MalformedEvent as error:
        log.error("dropping malformed event", extra={"reason": str(error), "size": len(data)})
        return None
