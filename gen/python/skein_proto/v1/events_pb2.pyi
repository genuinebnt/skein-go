import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from skein_proto.v1 import common_pb2 as _common_pb2
from skein_proto.v1 import identity_pb2 as _identity_pb2
from skein_proto.v1 import notifications_pb2 as _notifications_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Event(_message.Message):
    __slots__ = ("id", "occurred_at", "source", "ordering_key", "traceparent", "actor", "note_created", "note_renamed", "note_trashed", "note_restored", "note_purged", "blocks_changed", "links_changed", "access_changed", "notification_requested")
    ID_FIELD_NUMBER: _ClassVar[int]
    OCCURRED_AT_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    ORDERING_KEY_FIELD_NUMBER: _ClassVar[int]
    TRACEPARENT_FIELD_NUMBER: _ClassVar[int]
    ACTOR_FIELD_NUMBER: _ClassVar[int]
    NOTE_CREATED_FIELD_NUMBER: _ClassVar[int]
    NOTE_RENAMED_FIELD_NUMBER: _ClassVar[int]
    NOTE_TRASHED_FIELD_NUMBER: _ClassVar[int]
    NOTE_RESTORED_FIELD_NUMBER: _ClassVar[int]
    NOTE_PURGED_FIELD_NUMBER: _ClassVar[int]
    BLOCKS_CHANGED_FIELD_NUMBER: _ClassVar[int]
    LINKS_CHANGED_FIELD_NUMBER: _ClassVar[int]
    ACCESS_CHANGED_FIELD_NUMBER: _ClassVar[int]
    NOTIFICATION_REQUESTED_FIELD_NUMBER: _ClassVar[int]
    id: str
    occurred_at: _timestamp_pb2.Timestamp
    source: str
    ordering_key: str
    traceparent: str
    actor: _identity_pb2.Subject
    note_created: NoteCreated
    note_renamed: NoteRenamed
    note_trashed: NoteTrashed
    note_restored: NoteRestored
    note_purged: NotePurged
    blocks_changed: BlocksChanged
    links_changed: LinksChanged
    access_changed: AccessChanged
    notification_requested: NotificationRequested
    def __init__(self, id: _Optional[str] = ..., occurred_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., source: _Optional[str] = ..., ordering_key: _Optional[str] = ..., traceparent: _Optional[str] = ..., actor: _Optional[_Union[_identity_pb2.Subject, _Mapping]] = ..., note_created: _Optional[_Union[NoteCreated, _Mapping]] = ..., note_renamed: _Optional[_Union[NoteRenamed, _Mapping]] = ..., note_trashed: _Optional[_Union[NoteTrashed, _Mapping]] = ..., note_restored: _Optional[_Union[NoteRestored, _Mapping]] = ..., note_purged: _Optional[_Union[NotePurged, _Mapping]] = ..., blocks_changed: _Optional[_Union[BlocksChanged, _Mapping]] = ..., links_changed: _Optional[_Union[LinksChanged, _Mapping]] = ..., access_changed: _Optional[_Union[AccessChanged, _Mapping]] = ..., notification_requested: _Optional[_Union[NotificationRequested, _Mapping]] = ...) -> None: ...

class NoteCreated(_message.Message):
    __slots__ = ("note_id", "workspace_id", "title")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    workspace_id: str
    title: str
    def __init__(self, note_id: _Optional[str] = ..., workspace_id: _Optional[str] = ..., title: _Optional[str] = ...) -> None: ...

class NoteRenamed(_message.Message):
    __slots__ = ("note_id", "title")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    title: str
    def __init__(self, note_id: _Optional[str] = ..., title: _Optional[str] = ...) -> None: ...

class NoteTrashed(_message.Message):
    __slots__ = ("note_id",)
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    def __init__(self, note_id: _Optional[str] = ...) -> None: ...

class NoteRestored(_message.Message):
    __slots__ = ("note_id",)
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    def __init__(self, note_id: _Optional[str] = ...) -> None: ...

class NotePurged(_message.Message):
    __slots__ = ("note_id",)
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    def __init__(self, note_id: _Optional[str] = ...) -> None: ...

class BlocksChanged(_message.Message):
    __slots__ = ("note_id", "blocks", "removed_block_ids")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    BLOCKS_FIELD_NUMBER: _ClassVar[int]
    REMOVED_BLOCK_IDS_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    blocks: _containers.RepeatedCompositeFieldContainer[ChangedBlock]
    removed_block_ids: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, note_id: _Optional[str] = ..., blocks: _Optional[_Iterable[_Union[ChangedBlock, _Mapping]]] = ..., removed_block_ids: _Optional[_Iterable[str]] = ...) -> None: ...

class ChangedBlock(_message.Message):
    __slots__ = ("block_id", "kind", "language", "text")
    BLOCK_ID_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    block_id: str
    kind: str
    language: str
    text: str
    def __init__(self, block_id: _Optional[str] = ..., kind: _Optional[str] = ..., language: _Optional[str] = ..., text: _Optional[str] = ...) -> None: ...

class LinksChanged(_message.Message):
    __slots__ = ("note_id", "targets")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    TARGETS_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    targets: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, note_id: _Optional[str] = ..., targets: _Optional[_Iterable[str]] = ...) -> None: ...

class AccessChanged(_message.Message):
    __slots__ = ("workspace_id", "note_id", "user_id", "revoked")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    REVOKED_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    note_id: str
    user_id: str
    revoked: bool
    def __init__(self, workspace_id: _Optional[str] = ..., note_id: _Optional[str] = ..., user_id: _Optional[str] = ..., revoked: _Optional[bool] = ...) -> None: ...

class NotificationRequested(_message.Message):
    __slots__ = ("recipient_id", "kind", "title", "body", "location")
    RECIPIENT_ID_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    BODY_FIELD_NUMBER: _ClassVar[int]
    LOCATION_FIELD_NUMBER: _ClassVar[int]
    recipient_id: str
    kind: _notifications_pb2.NotificationKind
    title: str
    body: str
    location: _common_pb2.BlockRef
    def __init__(self, recipient_id: _Optional[str] = ..., kind: _Optional[_Union[_notifications_pb2.NotificationKind, str]] = ..., title: _Optional[str] = ..., body: _Optional[str] = ..., location: _Optional[_Union[_common_pb2.BlockRef, _Mapping]] = ...) -> None: ...
