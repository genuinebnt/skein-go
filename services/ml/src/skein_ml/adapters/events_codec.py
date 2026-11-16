"""Decode ``skein.v1.Event`` Protobuf messages into `skein_ml.app.events`.

Unknown or unconsumed payloads become `Ignored`, so a new event type
published upstream never breaks this consumer (forward compatibility, as
proto3 intends). A message that does not parse raises `MalformedEvent`;
the consumer acknowledges it after logging, because redelivering bytes
that can never parse would block the subscription forever.
"""

from __future__ import annotations

from ..app import events
from .grpc_stubs import events_pb2

__all__ = ["MalformedEvent", "decode_event", "encode_event"]


class MalformedEvent(ValueError):
    """The bytes are not a valid ``skein.v1.Event``."""


def decode_event(data: bytes, attributes: dict[str, str] | None = None) -> events.Event:
    """Parse an encoded envelope into an app `Event`."""
    message = events_pb2.Event()
    try:
        message.ParseFromString(data)
    except Exception as error:  # protobuf raises DecodeError, a private subclass
        raise MalformedEvent(str(error)) from error
    if not message.id:
        raise MalformedEvent("event has no id")
    return events.Event(id=message.id, payload=_payload(message), source=message.source,
                        traceparent=message.traceparent, attributes=dict(attributes or {}))


def _payload(message) -> events.Payload:
    which = message.WhichOneof("payload")
    if which == "note_created":
        p = message.note_created
        return events.NoteCreated(p.note_id, p.workspace_id, p.title)
    if which == "note_renamed":
        return events.NoteRenamed(message.note_renamed.note_id, message.note_renamed.title)
    if which == "note_trashed":
        return events.NoteTrashed(message.note_trashed.note_id)
    if which == "note_restored":
        return events.NoteRestored(message.note_restored.note_id)
    if which == "note_purged":
        return events.NotePurged(message.note_purged.note_id)
    if which == "blocks_changed":
        p = message.blocks_changed
        return events.BlocksChanged(
            note_id=p.note_id,
            blocks=tuple(events.ChangedBlock(b.block_id, b.text, b.kind, b.language)
                         for b in p.blocks),
            removed_block_ids=tuple(p.removed_block_ids))
    if which == "links_changed":
        return events.LinksChanged(message.links_changed.note_id,
                                   tuple(message.links_changed.targets))
    return events.Ignored()


def encode_event(event_id: str, payload: object, traceparent: str = "",
                 source: str = "documents") -> bytes:
    """Build an encoded envelope (for tests and local tooling)."""
    message = events_pb2.Event(id=event_id, source=source, traceparent=traceparent)
    match payload:
        case events.NoteCreated():
            message.note_created.note_id = payload.note_id
            message.note_created.workspace_id = payload.workspace_id
            message.note_created.title = payload.title
        case events.NoteRenamed():
            message.note_renamed.note_id = payload.note_id
            message.note_renamed.title = payload.title
        case events.NoteTrashed():
            message.note_trashed.note_id = payload.note_id
        case events.NoteRestored():
            message.note_restored.note_id = payload.note_id
        case events.NotePurged():
            message.note_purged.note_id = payload.note_id
        case events.BlocksChanged():
            message.blocks_changed.note_id = payload.note_id
            for block in payload.blocks:
                message.blocks_changed.blocks.add(block_id=block.block_id, text=block.text,
                                                  kind=block.block_kind, language=block.language)
            message.blocks_changed.removed_block_ids.extend(payload.removed_block_ids)
        case events.LinksChanged():
            message.links_changed.note_id = payload.note_id
            message.links_changed.targets.extend(payload.targets)
        case _:
            raise TypeError(f"cannot encode {type(payload).__name__}")
    return message.SerializeToString()
