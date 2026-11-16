"""Inbound domain events, decoupled from their Protobuf encoding.

Adapters decode ``skein.v1.Event`` into these types so the use cases never
import generated code. Each payload is its own frozen dataclass with a
``kind`` tag (ADR-0012: kind tags for sum types port to Rust enums and Go
switch statements without reflection). Payloads ml does not consume decode
to `Ignored`.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum

__all__ = [
    "BlocksChanged",
    "ChangedBlock",
    "Event",
    "EventKind",
    "Ignored",
    "LinksChanged",
    "NoteCreated",
    "NotePurged",
    "NoteRenamed",
    "NoteRestored",
    "NoteTrashed",
    "Payload",
]


class EventKind(StrEnum):
    """Discriminator of an event payload."""

    NOTE_CREATED = "note_created"
    NOTE_RENAMED = "note_renamed"
    NOTE_TRASHED = "note_trashed"
    NOTE_RESTORED = "note_restored"
    NOTE_PURGED = "note_purged"
    BLOCKS_CHANGED = "blocks_changed"
    LINKS_CHANGED = "links_changed"
    IGNORED = "ignored"


@dataclass(frozen=True)
class NoteCreated:
    """A note exists in a workspace."""

    note_id: str
    workspace_id: str
    title: str
    kind: EventKind = EventKind.NOTE_CREATED


@dataclass(frozen=True)
class NoteRenamed:
    """A note's title changed."""

    note_id: str
    title: str
    kind: EventKind = EventKind.NOTE_RENAMED


@dataclass(frozen=True)
class NoteTrashed:
    """A note moved to the trash: hide it from results, keep its data."""

    note_id: str
    kind: EventKind = EventKind.NOTE_TRASHED


@dataclass(frozen=True)
class NoteRestored:
    """A note came back from the trash."""

    note_id: str
    kind: EventKind = EventKind.NOTE_RESTORED


@dataclass(frozen=True)
class NotePurged:
    """A note is gone for good: erase everything held about it."""

    note_id: str
    kind: EventKind = EventKind.NOTE_PURGED


@dataclass(frozen=True)
class ChangedBlock:
    """The current text of one block."""

    block_id: str
    text: str
    block_kind: str = "paragraph"
    language: str = ""


@dataclass(frozen=True)
class BlocksChanged:
    """Blocks whose text changed and blocks that were removed."""

    note_id: str
    blocks: tuple[ChangedBlock, ...] = ()
    removed_block_ids: tuple[str, ...] = ()
    kind: EventKind = EventKind.BLOCKS_CHANGED


@dataclass(frozen=True)
class LinksChanged:
    """The note's complete outbound link set."""

    note_id: str
    targets: tuple[str, ...] = ()
    kind: EventKind = EventKind.LINKS_CHANGED


@dataclass(frozen=True)
class Ignored:
    """A payload ml does not consume (identity, notifications, ...)."""

    kind: EventKind = EventKind.IGNORED


Payload = (
    NoteCreated
    | NoteRenamed
    | NoteTrashed
    | NoteRestored
    | NotePurged
    | BlocksChanged
    | LinksChanged
    | Ignored
)


@dataclass(frozen=True)
class Event:
    """The envelope: ``id`` is the deduplication key, ``traceparent`` continues the trace."""

    id: str
    payload: Payload
    source: str = ""
    traceparent: str = ""
    attributes: dict[str, str] = field(default_factory=dict)
