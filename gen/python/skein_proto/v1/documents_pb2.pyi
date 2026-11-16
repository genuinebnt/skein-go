import datetime

from google.api import annotations_pb2 as _annotations_pb2
from google.api import field_behavior_pb2 as _field_behavior_pb2
from google.protobuf import timestamp_pb2 as _timestamp_pb2
from skein_proto.protoc_gen_openapiv2.options import annotations_pb2 as _annotations_pb2_1
from skein_proto.v1 import common_pb2 as _common_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Note(_message.Message):
    __slots__ = ("id", "workspace_id", "title", "created_by", "created_at", "updated_at", "trashed")
    ID_FIELD_NUMBER: _ClassVar[int]
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    TRASHED_FIELD_NUMBER: _ClassVar[int]
    id: str
    workspace_id: str
    title: str
    created_by: str
    created_at: _timestamp_pb2.Timestamp
    updated_at: _timestamp_pb2.Timestamp
    trashed: bool
    def __init__(self, id: _Optional[str] = ..., workspace_id: _Optional[str] = ..., title: _Optional[str] = ..., created_by: _Optional[str] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., updated_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., trashed: _Optional[bool] = ...) -> None: ...

class VersionVector(_message.Message):
    __slots__ = ("entries",)
    ENTRIES_FIELD_NUMBER: _ClassVar[int]
    entries: _containers.RepeatedCompositeFieldContainer[VersionEntry]
    def __init__(self, entries: _Optional[_Iterable[_Union[VersionEntry, _Mapping]]] = ...) -> None: ...

class VersionEntry(_message.Message):
    __slots__ = ("replica_id", "counter")
    REPLICA_ID_FIELD_NUMBER: _ClassVar[int]
    COUNTER_FIELD_NUMBER: _ClassVar[int]
    replica_id: int
    counter: int
    def __init__(self, replica_id: _Optional[int] = ..., counter: _Optional[int] = ...) -> None: ...

class CreateNoteRequest(_message.Message):
    __slots__ = ("workspace_id", "title", "markdown")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    MARKDOWN_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    title: str
    markdown: str
    def __init__(self, workspace_id: _Optional[str] = ..., title: _Optional[str] = ..., markdown: _Optional[str] = ...) -> None: ...

class CreateNoteResponse(_message.Message):
    __slots__ = ("note",)
    NOTE_FIELD_NUMBER: _ClassVar[int]
    note: Note
    def __init__(self, note: _Optional[_Union[Note, _Mapping]] = ...) -> None: ...

class GetNoteRequest(_message.Message):
    __slots__ = ("note_id", "include_markdown")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_MARKDOWN_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    include_markdown: bool
    def __init__(self, note_id: _Optional[str] = ..., include_markdown: _Optional[bool] = ...) -> None: ...

class GetNoteResponse(_message.Message):
    __slots__ = ("note", "markdown", "version")
    NOTE_FIELD_NUMBER: _ClassVar[int]
    MARKDOWN_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    note: Note
    markdown: str
    version: VersionVector
    def __init__(self, note: _Optional[_Union[Note, _Mapping]] = ..., markdown: _Optional[str] = ..., version: _Optional[_Union[VersionVector, _Mapping]] = ...) -> None: ...

class ListNotesRequest(_message.Message):
    __slots__ = ("workspace_id", "page", "include_trashed")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_TRASHED_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    page: _common_pb2.PageRequest
    include_trashed: bool
    def __init__(self, workspace_id: _Optional[str] = ..., page: _Optional[_Union[_common_pb2.PageRequest, _Mapping]] = ..., include_trashed: _Optional[bool] = ...) -> None: ...

class ListNotesResponse(_message.Message):
    __slots__ = ("notes", "page")
    NOTES_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    notes: _containers.RepeatedCompositeFieldContainer[Note]
    page: _common_pb2.PageResponse
    def __init__(self, notes: _Optional[_Iterable[_Union[Note, _Mapping]]] = ..., page: _Optional[_Union[_common_pb2.PageResponse, _Mapping]] = ...) -> None: ...

class DeleteNoteRequest(_message.Message):
    __slots__ = ("note_id",)
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    def __init__(self, note_id: _Optional[str] = ...) -> None: ...

class DeleteNoteResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class RestoreNoteRequest(_message.Message):
    __slots__ = ("note_id",)
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    def __init__(self, note_id: _Optional[str] = ...) -> None: ...

class RestoreNoteResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class RenameNoteRequest(_message.Message):
    __slots__ = ("note_id", "title")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    title: str
    def __init__(self, note_id: _Optional[str] = ..., title: _Optional[str] = ...) -> None: ...

class RenameNoteResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ApplyOpsRequest(_message.Message):
    __slots__ = ("note_id", "replica_id", "ops")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    REPLICA_ID_FIELD_NUMBER: _ClassVar[int]
    OPS_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    replica_id: int
    ops: bytes
    def __init__(self, note_id: _Optional[str] = ..., replica_id: _Optional[int] = ..., ops: _Optional[bytes] = ...) -> None: ...

class ApplyOpsResponse(_message.Message):
    __slots__ = ("version",)
    VERSION_FIELD_NUMBER: _ClassVar[int]
    version: VersionVector
    def __init__(self, version: _Optional[_Union[VersionVector, _Mapping]] = ...) -> None: ...

class SyncRequest(_message.Message):
    __slots__ = ("hello", "ops", "presence")
    HELLO_FIELD_NUMBER: _ClassVar[int]
    OPS_FIELD_NUMBER: _ClassVar[int]
    PRESENCE_FIELD_NUMBER: _ClassVar[int]
    hello: SyncHello
    ops: bytes
    presence: Presence
    def __init__(self, hello: _Optional[_Union[SyncHello, _Mapping]] = ..., ops: _Optional[bytes] = ..., presence: _Optional[_Union[Presence, _Mapping]] = ...) -> None: ...

class SyncHello(_message.Message):
    __slots__ = ("note_id", "replica_id", "version")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    REPLICA_ID_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    replica_id: int
    version: VersionVector
    def __init__(self, note_id: _Optional[str] = ..., replica_id: _Optional[int] = ..., version: _Optional[_Union[VersionVector, _Mapping]] = ...) -> None: ...

class SyncResponse(_message.Message):
    __slots__ = ("ops", "presence", "rejected")
    OPS_FIELD_NUMBER: _ClassVar[int]
    PRESENCE_FIELD_NUMBER: _ClassVar[int]
    REJECTED_FIELD_NUMBER: _ClassVar[int]
    ops: bytes
    presence: Presence
    rejected: SyncRejected
    def __init__(self, ops: _Optional[bytes] = ..., presence: _Optional[_Union[Presence, _Mapping]] = ..., rejected: _Optional[_Union[SyncRejected, _Mapping]] = ...) -> None: ...

class SyncRejected(_message.Message):
    __slots__ = ("ops", "reason")
    OPS_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    ops: bytes
    reason: str
    def __init__(self, ops: _Optional[bytes] = ..., reason: _Optional[str] = ...) -> None: ...

class Presence(_message.Message):
    __slots__ = ("user_id", "replica_id", "anchor", "head")
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    REPLICA_ID_FIELD_NUMBER: _ClassVar[int]
    ANCHOR_FIELD_NUMBER: _ClassVar[int]
    HEAD_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    replica_id: int
    anchor: str
    head: str
    def __init__(self, user_id: _Optional[str] = ..., replica_id: _Optional[int] = ..., anchor: _Optional[str] = ..., head: _Optional[str] = ...) -> None: ...

class ListVersionsRequest(_message.Message):
    __slots__ = ("note_id", "page")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    page: _common_pb2.PageRequest
    def __init__(self, note_id: _Optional[str] = ..., page: _Optional[_Union[_common_pb2.PageRequest, _Mapping]] = ...) -> None: ...

class ListVersionsResponse(_message.Message):
    __slots__ = ("versions", "page")
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    versions: _containers.RepeatedCompositeFieldContainer[NoteVersion]
    page: _common_pb2.PageResponse
    def __init__(self, versions: _Optional[_Iterable[_Union[NoteVersion, _Mapping]]] = ..., page: _Optional[_Union[_common_pb2.PageResponse, _Mapping]] = ...) -> None: ...

class NoteVersion(_message.Message):
    __slots__ = ("version", "author_ids", "at")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    AUTHOR_IDS_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    version: VersionVector
    author_ids: _containers.RepeatedScalarFieldContainer[str]
    at: _timestamp_pb2.Timestamp
    def __init__(self, version: _Optional[_Union[VersionVector, _Mapping]] = ..., author_ids: _Optional[_Iterable[str]] = ..., at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class GetTextAtRequest(_message.Message):
    __slots__ = ("note_id", "version")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    version: VersionVector
    def __init__(self, note_id: _Optional[str] = ..., version: _Optional[_Union[VersionVector, _Mapping]] = ...) -> None: ...

class GetTextAtResponse(_message.Message):
    __slots__ = ("markdown",)
    MARKDOWN_FIELD_NUMBER: _ClassVar[int]
    markdown: str
    def __init__(self, markdown: _Optional[str] = ...) -> None: ...

class DiffRequest(_message.Message):
    __slots__ = ("note_id", "to")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    FROM_FIELD_NUMBER: _ClassVar[int]
    TO_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    to: VersionVector
    def __init__(self, note_id: _Optional[str] = ..., to: _Optional[_Union[VersionVector, _Mapping]] = ..., **kwargs) -> None: ...

class DiffResponse(_message.Message):
    __slots__ = ("hunks",)
    HUNKS_FIELD_NUMBER: _ClassVar[int]
    hunks: _containers.RepeatedCompositeFieldContainer[DiffHunk]
    def __init__(self, hunks: _Optional[_Iterable[_Union[DiffHunk, _Mapping]]] = ...) -> None: ...

class DiffHunk(_message.Message):
    __slots__ = ("kind", "text")
    class Kind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
        __slots__ = ()
        KIND_UNSPECIFIED: _ClassVar[DiffHunk.Kind]
        KIND_EQUAL: _ClassVar[DiffHunk.Kind]
        KIND_INSERT: _ClassVar[DiffHunk.Kind]
        KIND_DELETE: _ClassVar[DiffHunk.Kind]
    KIND_UNSPECIFIED: DiffHunk.Kind
    KIND_EQUAL: DiffHunk.Kind
    KIND_INSERT: DiffHunk.Kind
    KIND_DELETE: DiffHunk.Kind
    KIND_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    kind: DiffHunk.Kind
    text: str
    def __init__(self, kind: _Optional[_Union[DiffHunk.Kind, str]] = ..., text: _Optional[str] = ...) -> None: ...

class BlameRequest(_message.Message):
    __slots__ = ("note_id",)
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    def __init__(self, note_id: _Optional[str] = ...) -> None: ...

class BlameResponse(_message.Message):
    __slots__ = ("spans",)
    SPANS_FIELD_NUMBER: _ClassVar[int]
    spans: _containers.RepeatedCompositeFieldContainer[BlameSpan]
    def __init__(self, spans: _Optional[_Iterable[_Union[BlameSpan, _Mapping]]] = ...) -> None: ...

class BlameSpan(_message.Message):
    __slots__ = ("start", "end", "user_id", "agent_run_id", "at")
    START_FIELD_NUMBER: _ClassVar[int]
    END_FIELD_NUMBER: _ClassVar[int]
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    AGENT_RUN_ID_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    start: int
    end: int
    user_id: str
    agent_run_id: str
    at: _timestamp_pb2.Timestamp
    def __init__(self, start: _Optional[int] = ..., end: _Optional[int] = ..., user_id: _Optional[str] = ..., agent_run_id: _Optional[str] = ..., at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class UndoRequest(_message.Message):
    __slots__ = ("note_id", "replica_id")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    REPLICA_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    replica_id: int
    def __init__(self, note_id: _Optional[str] = ..., replica_id: _Optional[int] = ...) -> None: ...

class UndoResponse(_message.Message):
    __slots__ = ("ops",)
    OPS_FIELD_NUMBER: _ClassVar[int]
    ops: bytes
    def __init__(self, ops: _Optional[bytes] = ...) -> None: ...

class RedoRequest(_message.Message):
    __slots__ = ("note_id", "replica_id")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    REPLICA_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    replica_id: int
    def __init__(self, note_id: _Optional[str] = ..., replica_id: _Optional[int] = ...) -> None: ...

class RedoResponse(_message.Message):
    __slots__ = ("ops",)
    OPS_FIELD_NUMBER: _ClassVar[int]
    ops: bytes
    def __init__(self, ops: _Optional[bytes] = ...) -> None: ...
