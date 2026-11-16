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

class NotificationKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    NOTIFICATION_KIND_UNSPECIFIED: _ClassVar[NotificationKind]
    NOTIFICATION_KIND_MENTION: _ClassVar[NotificationKind]
    NOTIFICATION_KIND_COMMENT: _ClassVar[NotificationKind]
    NOTIFICATION_KIND_SHARE: _ClassVar[NotificationKind]
    NOTIFICATION_KIND_AGENT_RUN_FINISHED: _ClassVar[NotificationKind]
    NOTIFICATION_KIND_ACCESS_CHANGED: _ClassVar[NotificationKind]
NOTIFICATION_KIND_UNSPECIFIED: NotificationKind
NOTIFICATION_KIND_MENTION: NotificationKind
NOTIFICATION_KIND_COMMENT: NotificationKind
NOTIFICATION_KIND_SHARE: NotificationKind
NOTIFICATION_KIND_AGENT_RUN_FINISHED: NotificationKind
NOTIFICATION_KIND_ACCESS_CHANGED: NotificationKind

class Notification(_message.Message):
    __slots__ = ("id", "kind", "title", "body", "location", "actor_id", "created_at", "read")
    ID_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    BODY_FIELD_NUMBER: _ClassVar[int]
    LOCATION_FIELD_NUMBER: _ClassVar[int]
    ACTOR_ID_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    READ_FIELD_NUMBER: _ClassVar[int]
    id: str
    kind: NotificationKind
    title: str
    body: str
    location: _common_pb2.BlockRef
    actor_id: str
    created_at: _timestamp_pb2.Timestamp
    read: bool
    def __init__(self, id: _Optional[str] = ..., kind: _Optional[_Union[NotificationKind, str]] = ..., title: _Optional[str] = ..., body: _Optional[str] = ..., location: _Optional[_Union[_common_pb2.BlockRef, _Mapping]] = ..., actor_id: _Optional[str] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., read: _Optional[bool] = ...) -> None: ...

class ListInboxRequest(_message.Message):
    __slots__ = ("page", "unread_only")
    PAGE_FIELD_NUMBER: _ClassVar[int]
    UNREAD_ONLY_FIELD_NUMBER: _ClassVar[int]
    page: _common_pb2.PageRequest
    unread_only: bool
    def __init__(self, page: _Optional[_Union[_common_pb2.PageRequest, _Mapping]] = ..., unread_only: _Optional[bool] = ...) -> None: ...

class ListInboxResponse(_message.Message):
    __slots__ = ("notifications", "page", "unread")
    NOTIFICATIONS_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    UNREAD_FIELD_NUMBER: _ClassVar[int]
    notifications: _containers.RepeatedCompositeFieldContainer[Notification]
    page: _common_pb2.PageResponse
    unread: int
    def __init__(self, notifications: _Optional[_Iterable[_Union[Notification, _Mapping]]] = ..., page: _Optional[_Union[_common_pb2.PageResponse, _Mapping]] = ..., unread: _Optional[int] = ...) -> None: ...

class MarkReadRequest(_message.Message):
    __slots__ = ("ids",)
    IDS_FIELD_NUMBER: _ClassVar[int]
    ids: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, ids: _Optional[_Iterable[str]] = ...) -> None: ...

class MarkReadResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class SubscribeRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class SubscribeResponse(_message.Message):
    __slots__ = ("notification",)
    NOTIFICATION_FIELD_NUMBER: _ClassVar[int]
    notification: Notification
    def __init__(self, notification: _Optional[_Union[Notification, _Mapping]] = ...) -> None: ...

class Preference(_message.Message):
    __slots__ = ("kind", "in_app", "email")
    KIND_FIELD_NUMBER: _ClassVar[int]
    IN_APP_FIELD_NUMBER: _ClassVar[int]
    EMAIL_FIELD_NUMBER: _ClassVar[int]
    kind: NotificationKind
    in_app: bool
    email: bool
    def __init__(self, kind: _Optional[_Union[NotificationKind, str]] = ..., in_app: _Optional[bool] = ..., email: _Optional[bool] = ...) -> None: ...

class GetPreferencesRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GetPreferencesResponse(_message.Message):
    __slots__ = ("preferences",)
    PREFERENCES_FIELD_NUMBER: _ClassVar[int]
    preferences: _containers.RepeatedCompositeFieldContainer[Preference]
    def __init__(self, preferences: _Optional[_Iterable[_Union[Preference, _Mapping]]] = ...) -> None: ...

class UpdatePreferencesRequest(_message.Message):
    __slots__ = ("preferences",)
    PREFERENCES_FIELD_NUMBER: _ClassVar[int]
    preferences: _containers.RepeatedCompositeFieldContainer[Preference]
    def __init__(self, preferences: _Optional[_Iterable[_Union[Preference, _Mapping]]] = ...) -> None: ...

class UpdatePreferencesResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...
