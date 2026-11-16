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

class WorkspaceRole(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    WORKSPACE_ROLE_UNSPECIFIED: _ClassVar[WorkspaceRole]
    WORKSPACE_ROLE_GUEST: _ClassVar[WorkspaceRole]
    WORKSPACE_ROLE_MEMBER: _ClassVar[WorkspaceRole]
    WORKSPACE_ROLE_ADMIN: _ClassVar[WorkspaceRole]
    WORKSPACE_ROLE_OWNER: _ClassVar[WorkspaceRole]

class NoteRole(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    NOTE_ROLE_UNSPECIFIED: _ClassVar[NoteRole]
    NOTE_ROLE_VIEWER: _ClassVar[NoteRole]
    NOTE_ROLE_COMMENTER: _ClassVar[NoteRole]
    NOTE_ROLE_EDITOR: _ClassVar[NoteRole]

class Action(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ACTION_UNSPECIFIED: _ClassVar[Action]
    ACTION_NOTE_READ: _ClassVar[Action]
    ACTION_NOTE_COMMENT: _ClassVar[Action]
    ACTION_NOTE_EDIT: _ClassVar[Action]
    ACTION_NOTE_CREATE: _ClassVar[Action]
    ACTION_NOTE_DELETE: _ClassVar[Action]
    ACTION_NOTE_SHARE: _ClassVar[Action]
    ACTION_WORKSPACE_MANAGE_MEMBERS: _ClassVar[Action]
    ACTION_WORKSPACE_MANAGE_SETTINGS: _ClassVar[Action]
    ACTION_AGENT_RUN: _ClassVar[Action]

class OAuthProvider(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    O_AUTH_PROVIDER_UNSPECIFIED: _ClassVar[OAuthProvider]
    O_AUTH_PROVIDER_GOOGLE: _ClassVar[OAuthProvider]
    O_AUTH_PROVIDER_GITHUB: _ClassVar[OAuthProvider]
WORKSPACE_ROLE_UNSPECIFIED: WorkspaceRole
WORKSPACE_ROLE_GUEST: WorkspaceRole
WORKSPACE_ROLE_MEMBER: WorkspaceRole
WORKSPACE_ROLE_ADMIN: WorkspaceRole
WORKSPACE_ROLE_OWNER: WorkspaceRole
NOTE_ROLE_UNSPECIFIED: NoteRole
NOTE_ROLE_VIEWER: NoteRole
NOTE_ROLE_COMMENTER: NoteRole
NOTE_ROLE_EDITOR: NoteRole
ACTION_UNSPECIFIED: Action
ACTION_NOTE_READ: Action
ACTION_NOTE_COMMENT: Action
ACTION_NOTE_EDIT: Action
ACTION_NOTE_CREATE: Action
ACTION_NOTE_DELETE: Action
ACTION_NOTE_SHARE: Action
ACTION_WORKSPACE_MANAGE_MEMBERS: Action
ACTION_WORKSPACE_MANAGE_SETTINGS: Action
ACTION_AGENT_RUN: Action
O_AUTH_PROVIDER_UNSPECIFIED: OAuthProvider
O_AUTH_PROVIDER_GOOGLE: OAuthProvider
O_AUTH_PROVIDER_GITHUB: OAuthProvider

class User(_message.Message):
    __slots__ = ("id", "email", "display_name", "created_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    EMAIL_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    email: str
    display_name: str
    created_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., email: _Optional[str] = ..., display_name: _Optional[str] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class Session(_message.Message):
    __slots__ = ("access_token", "access_expires_at", "refresh_token", "refresh_expires_at")
    ACCESS_TOKEN_FIELD_NUMBER: _ClassVar[int]
    ACCESS_EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    REFRESH_TOKEN_FIELD_NUMBER: _ClassVar[int]
    REFRESH_EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    access_token: str
    access_expires_at: _timestamp_pb2.Timestamp
    refresh_token: str
    refresh_expires_at: _timestamp_pb2.Timestamp
    def __init__(self, access_token: _Optional[str] = ..., access_expires_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., refresh_token: _Optional[str] = ..., refresh_expires_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class Subject(_message.Message):
    __slots__ = ("user_id", "agent_run_id")
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    AGENT_RUN_ID_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    agent_run_id: str
    def __init__(self, user_id: _Optional[str] = ..., agent_run_id: _Optional[str] = ...) -> None: ...

class Member(_message.Message):
    __slots__ = ("user", "role")
    USER_FIELD_NUMBER: _ClassVar[int]
    ROLE_FIELD_NUMBER: _ClassVar[int]
    user: User
    role: WorkspaceRole
    def __init__(self, user: _Optional[_Union[User, _Mapping]] = ..., role: _Optional[_Union[WorkspaceRole, str]] = ...) -> None: ...

class SignUpRequest(_message.Message):
    __slots__ = ("email", "password", "display_name")
    EMAIL_FIELD_NUMBER: _ClassVar[int]
    PASSWORD_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    email: str
    password: str
    display_name: str
    def __init__(self, email: _Optional[str] = ..., password: _Optional[str] = ..., display_name: _Optional[str] = ...) -> None: ...

class SignUpResponse(_message.Message):
    __slots__ = ("user", "session")
    USER_FIELD_NUMBER: _ClassVar[int]
    SESSION_FIELD_NUMBER: _ClassVar[int]
    user: User
    session: Session
    def __init__(self, user: _Optional[_Union[User, _Mapping]] = ..., session: _Optional[_Union[Session, _Mapping]] = ...) -> None: ...

class SignInRequest(_message.Message):
    __slots__ = ("email", "password")
    EMAIL_FIELD_NUMBER: _ClassVar[int]
    PASSWORD_FIELD_NUMBER: _ClassVar[int]
    email: str
    password: str
    def __init__(self, email: _Optional[str] = ..., password: _Optional[str] = ...) -> None: ...

class SignInResponse(_message.Message):
    __slots__ = ("user", "session")
    USER_FIELD_NUMBER: _ClassVar[int]
    SESSION_FIELD_NUMBER: _ClassVar[int]
    user: User
    session: Session
    def __init__(self, user: _Optional[_Union[User, _Mapping]] = ..., session: _Optional[_Union[Session, _Mapping]] = ...) -> None: ...

class SignInWithOAuthRequest(_message.Message):
    __slots__ = ("provider", "code", "code_verifier", "redirect_uri")
    PROVIDER_FIELD_NUMBER: _ClassVar[int]
    CODE_FIELD_NUMBER: _ClassVar[int]
    CODE_VERIFIER_FIELD_NUMBER: _ClassVar[int]
    REDIRECT_URI_FIELD_NUMBER: _ClassVar[int]
    provider: OAuthProvider
    code: str
    code_verifier: str
    redirect_uri: str
    def __init__(self, provider: _Optional[_Union[OAuthProvider, str]] = ..., code: _Optional[str] = ..., code_verifier: _Optional[str] = ..., redirect_uri: _Optional[str] = ...) -> None: ...

class SignInWithOAuthResponse(_message.Message):
    __slots__ = ("user", "session", "created")
    USER_FIELD_NUMBER: _ClassVar[int]
    SESSION_FIELD_NUMBER: _ClassVar[int]
    CREATED_FIELD_NUMBER: _ClassVar[int]
    user: User
    session: Session
    created: bool
    def __init__(self, user: _Optional[_Union[User, _Mapping]] = ..., session: _Optional[_Union[Session, _Mapping]] = ..., created: _Optional[bool] = ...) -> None: ...

class RefreshRequest(_message.Message):
    __slots__ = ("refresh_token",)
    REFRESH_TOKEN_FIELD_NUMBER: _ClassVar[int]
    refresh_token: str
    def __init__(self, refresh_token: _Optional[str] = ...) -> None: ...

class RefreshResponse(_message.Message):
    __slots__ = ("session",)
    SESSION_FIELD_NUMBER: _ClassVar[int]
    session: Session
    def __init__(self, session: _Optional[_Union[Session, _Mapping]] = ...) -> None: ...

class SignOutRequest(_message.Message):
    __slots__ = ("refresh_token",)
    REFRESH_TOKEN_FIELD_NUMBER: _ClassVar[int]
    refresh_token: str
    def __init__(self, refresh_token: _Optional[str] = ...) -> None: ...

class SignOutResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GetUserRequest(_message.Message):
    __slots__ = ("user_id",)
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    def __init__(self, user_id: _Optional[str] = ...) -> None: ...

class GetUserResponse(_message.Message):
    __slots__ = ("user",)
    USER_FIELD_NUMBER: _ClassVar[int]
    user: User
    def __init__(self, user: _Optional[_Union[User, _Mapping]] = ...) -> None: ...

class CreateWorkspaceRequest(_message.Message):
    __slots__ = ("name",)
    NAME_FIELD_NUMBER: _ClassVar[int]
    name: str
    def __init__(self, name: _Optional[str] = ...) -> None: ...

class CreateWorkspaceResponse(_message.Message):
    __slots__ = ("workspace_id",)
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    def __init__(self, workspace_id: _Optional[str] = ...) -> None: ...

class ListMembersRequest(_message.Message):
    __slots__ = ("workspace_id", "page")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    page: _common_pb2.PageRequest
    def __init__(self, workspace_id: _Optional[str] = ..., page: _Optional[_Union[_common_pb2.PageRequest, _Mapping]] = ...) -> None: ...

class ListMembersResponse(_message.Message):
    __slots__ = ("members", "page")
    MEMBERS_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    members: _containers.RepeatedCompositeFieldContainer[Member]
    page: _common_pb2.PageResponse
    def __init__(self, members: _Optional[_Iterable[_Union[Member, _Mapping]]] = ..., page: _Optional[_Union[_common_pb2.PageResponse, _Mapping]] = ...) -> None: ...

class SetWorkspaceRoleRequest(_message.Message):
    __slots__ = ("workspace_id", "user_id", "role")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    ROLE_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    user_id: str
    role: WorkspaceRole
    def __init__(self, workspace_id: _Optional[str] = ..., user_id: _Optional[str] = ..., role: _Optional[_Union[WorkspaceRole, str]] = ...) -> None: ...

class SetWorkspaceRoleResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class RemoveMemberRequest(_message.Message):
    __slots__ = ("workspace_id", "user_id")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    user_id: str
    def __init__(self, workspace_id: _Optional[str] = ..., user_id: _Optional[str] = ...) -> None: ...

class RemoveMemberResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class SetNoteRoleRequest(_message.Message):
    __slots__ = ("note_id", "user_id", "role")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    ROLE_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    user_id: str
    role: NoteRole
    def __init__(self, note_id: _Optional[str] = ..., user_id: _Optional[str] = ..., role: _Optional[_Union[NoteRole, str]] = ...) -> None: ...

class SetNoteRoleResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class CreateShareLinkRequest(_message.Message):
    __slots__ = ("note_id", "role", "expires_at")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    ROLE_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    role: NoteRole
    expires_at: _timestamp_pb2.Timestamp
    def __init__(self, note_id: _Optional[str] = ..., role: _Optional[_Union[NoteRole, str]] = ..., expires_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class CreateShareLinkResponse(_message.Message):
    __slots__ = ("link_id", "token")
    LINK_ID_FIELD_NUMBER: _ClassVar[int]
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    link_id: str
    token: str
    def __init__(self, link_id: _Optional[str] = ..., token: _Optional[str] = ...) -> None: ...

class RevokeShareLinkRequest(_message.Message):
    __slots__ = ("link_id",)
    LINK_ID_FIELD_NUMBER: _ClassVar[int]
    link_id: str
    def __init__(self, link_id: _Optional[str] = ...) -> None: ...

class RevokeShareLinkResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class AuthorizeRequest(_message.Message):
    __slots__ = ("subject", "checks")
    SUBJECT_FIELD_NUMBER: _ClassVar[int]
    CHECKS_FIELD_NUMBER: _ClassVar[int]
    subject: Subject
    checks: _containers.RepeatedCompositeFieldContainer[Check]
    def __init__(self, subject: _Optional[_Union[Subject, _Mapping]] = ..., checks: _Optional[_Iterable[_Union[Check, _Mapping]]] = ...) -> None: ...

class Check(_message.Message):
    __slots__ = ("action", "resource_id")
    ACTION_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_ID_FIELD_NUMBER: _ClassVar[int]
    action: Action
    resource_id: str
    def __init__(self, action: _Optional[_Union[Action, str]] = ..., resource_id: _Optional[str] = ...) -> None: ...

class AuthorizeResponse(_message.Message):
    __slots__ = ("allowed",)
    ALLOWED_FIELD_NUMBER: _ClassVar[int]
    allowed: _containers.RepeatedScalarFieldContainer[bool]
    def __init__(self, allowed: _Optional[_Iterable[bool]] = ...) -> None: ...

class ListReadableNotesRequest(_message.Message):
    __slots__ = ("subject", "workspace_id")
    SUBJECT_FIELD_NUMBER: _ClassVar[int]
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    subject: Subject
    workspace_id: str
    def __init__(self, subject: _Optional[_Union[Subject, _Mapping]] = ..., workspace_id: _Optional[str] = ...) -> None: ...

class ListReadableNotesResponse(_message.Message):
    __slots__ = ("note_ids",)
    NOTE_IDS_FIELD_NUMBER: _ClassVar[int]
    note_ids: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, note_ids: _Optional[_Iterable[str]] = ...) -> None: ...

class IssueDelegatedTokenRequest(_message.Message):
    __slots__ = ("user_id", "agent_run_id", "expires_at")
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    AGENT_RUN_ID_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    agent_run_id: str
    expires_at: _timestamp_pb2.Timestamp
    def __init__(self, user_id: _Optional[str] = ..., agent_run_id: _Optional[str] = ..., expires_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class IssueDelegatedTokenResponse(_message.Message):
    __slots__ = ("access_token",)
    ACCESS_TOKEN_FIELD_NUMBER: _ClassVar[int]
    access_token: str
    def __init__(self, access_token: _Optional[str] = ...) -> None: ...

class RegisterReplicaRequest(_message.Message):
    __slots__ = ("replica_id",)
    REPLICA_ID_FIELD_NUMBER: _ClassVar[int]
    replica_id: int
    def __init__(self, replica_id: _Optional[int] = ...) -> None: ...

class RegisterReplicaResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...
