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

class CreateChatRequest(_message.Message):
    __slots__ = ("workspace_id", "note_id")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    note_id: str
    def __init__(self, workspace_id: _Optional[str] = ..., note_id: _Optional[str] = ...) -> None: ...

class CreateChatResponse(_message.Message):
    __slots__ = ("chat_id",)
    CHAT_ID_FIELD_NUMBER: _ClassVar[int]
    chat_id: str
    def __init__(self, chat_id: _Optional[str] = ...) -> None: ...

class ListChatsRequest(_message.Message):
    __slots__ = ("workspace_id", "page")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    page: _common_pb2.PageRequest
    def __init__(self, workspace_id: _Optional[str] = ..., page: _Optional[_Union[_common_pb2.PageRequest, _Mapping]] = ...) -> None: ...

class ListChatsResponse(_message.Message):
    __slots__ = ("chats", "page")
    CHATS_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    chats: _containers.RepeatedCompositeFieldContainer[ChatSummary]
    page: _common_pb2.PageResponse
    def __init__(self, chats: _Optional[_Iterable[_Union[ChatSummary, _Mapping]]] = ..., page: _Optional[_Union[_common_pb2.PageResponse, _Mapping]] = ...) -> None: ...

class ChatSummary(_message.Message):
    __slots__ = ("chat_id", "title", "updated_at")
    CHAT_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    chat_id: str
    title: str
    updated_at: _timestamp_pb2.Timestamp
    def __init__(self, chat_id: _Optional[str] = ..., title: _Optional[str] = ..., updated_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class SendMessageRequest(_message.Message):
    __slots__ = ("chat_id", "text")
    CHAT_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    chat_id: str
    text: str
    def __init__(self, chat_id: _Optional[str] = ..., text: _Optional[str] = ...) -> None: ...

class SendMessageResponse(_message.Message):
    __slots__ = ("run_id", "text_delta", "tool_call", "tool_result", "confirmation", "citation", "finished")
    RUN_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_DELTA_FIELD_NUMBER: _ClassVar[int]
    TOOL_CALL_FIELD_NUMBER: _ClassVar[int]
    TOOL_RESULT_FIELD_NUMBER: _ClassVar[int]
    CONFIRMATION_FIELD_NUMBER: _ClassVar[int]
    CITATION_FIELD_NUMBER: _ClassVar[int]
    FINISHED_FIELD_NUMBER: _ClassVar[int]
    run_id: str
    text_delta: str
    tool_call: ToolCall
    tool_result: ToolResult
    confirmation: ConfirmationRequest
    citation: Citation
    finished: RunFinished
    def __init__(self, run_id: _Optional[str] = ..., text_delta: _Optional[str] = ..., tool_call: _Optional[_Union[ToolCall, _Mapping]] = ..., tool_result: _Optional[_Union[ToolResult, _Mapping]] = ..., confirmation: _Optional[_Union[ConfirmationRequest, _Mapping]] = ..., citation: _Optional[_Union[Citation, _Mapping]] = ..., finished: _Optional[_Union[RunFinished, _Mapping]] = ...) -> None: ...

class ToolCall(_message.Message):
    __slots__ = ("call_id", "tool", "arguments_json")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    TOOL_FIELD_NUMBER: _ClassVar[int]
    ARGUMENTS_JSON_FIELD_NUMBER: _ClassVar[int]
    call_id: str
    tool: str
    arguments_json: str
    def __init__(self, call_id: _Optional[str] = ..., tool: _Optional[str] = ..., arguments_json: _Optional[str] = ...) -> None: ...

class ToolResult(_message.Message):
    __slots__ = ("call_id", "summary", "is_error")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    SUMMARY_FIELD_NUMBER: _ClassVar[int]
    IS_ERROR_FIELD_NUMBER: _ClassVar[int]
    call_id: str
    summary: str
    is_error: bool
    def __init__(self, call_id: _Optional[str] = ..., summary: _Optional[str] = ..., is_error: _Optional[bool] = ...) -> None: ...

class ConfirmationRequest(_message.Message):
    __slots__ = ("confirmation_id", "description", "tool", "arguments_json")
    CONFIRMATION_ID_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    TOOL_FIELD_NUMBER: _ClassVar[int]
    ARGUMENTS_JSON_FIELD_NUMBER: _ClassVar[int]
    confirmation_id: str
    description: str
    tool: str
    arguments_json: str
    def __init__(self, confirmation_id: _Optional[str] = ..., description: _Optional[str] = ..., tool: _Optional[str] = ..., arguments_json: _Optional[str] = ...) -> None: ...

class Citation(_message.Message):
    __slots__ = ("location", "quote")
    LOCATION_FIELD_NUMBER: _ClassVar[int]
    QUOTE_FIELD_NUMBER: _ClassVar[int]
    location: _common_pb2.BlockRef
    quote: str
    def __init__(self, location: _Optional[_Union[_common_pb2.BlockRef, _Mapping]] = ..., quote: _Optional[str] = ...) -> None: ...

class RunFinished(_message.Message):
    __slots__ = ("outcome", "steps", "input_tokens", "output_tokens")
    class Outcome(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
        __slots__ = ()
        OUTCOME_UNSPECIFIED: _ClassVar[RunFinished.Outcome]
        OUTCOME_COMPLETED: _ClassVar[RunFinished.Outcome]
        OUTCOME_CANCELLED: _ClassVar[RunFinished.Outcome]
        OUTCOME_BUDGET_EXCEEDED: _ClassVar[RunFinished.Outcome]
        OUTCOME_FAILED: _ClassVar[RunFinished.Outcome]
    OUTCOME_UNSPECIFIED: RunFinished.Outcome
    OUTCOME_COMPLETED: RunFinished.Outcome
    OUTCOME_CANCELLED: RunFinished.Outcome
    OUTCOME_BUDGET_EXCEEDED: RunFinished.Outcome
    OUTCOME_FAILED: RunFinished.Outcome
    OUTCOME_FIELD_NUMBER: _ClassVar[int]
    STEPS_FIELD_NUMBER: _ClassVar[int]
    INPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    OUTPUT_TOKENS_FIELD_NUMBER: _ClassVar[int]
    outcome: RunFinished.Outcome
    steps: int
    input_tokens: int
    output_tokens: int
    def __init__(self, outcome: _Optional[_Union[RunFinished.Outcome, str]] = ..., steps: _Optional[int] = ..., input_tokens: _Optional[int] = ..., output_tokens: _Optional[int] = ...) -> None: ...

class ConfirmRequest(_message.Message):
    __slots__ = ("run_id", "confirmation_id", "approved")
    RUN_ID_FIELD_NUMBER: _ClassVar[int]
    CONFIRMATION_ID_FIELD_NUMBER: _ClassVar[int]
    APPROVED_FIELD_NUMBER: _ClassVar[int]
    run_id: str
    confirmation_id: str
    approved: bool
    def __init__(self, run_id: _Optional[str] = ..., confirmation_id: _Optional[str] = ..., approved: _Optional[bool] = ...) -> None: ...

class ConfirmResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class CancelRunRequest(_message.Message):
    __slots__ = ("run_id",)
    RUN_ID_FIELD_NUMBER: _ClassVar[int]
    run_id: str
    def __init__(self, run_id: _Optional[str] = ...) -> None: ...

class CancelRunResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GetRunRequest(_message.Message):
    __slots__ = ("run_id",)
    RUN_ID_FIELD_NUMBER: _ClassVar[int]
    run_id: str
    def __init__(self, run_id: _Optional[str] = ...) -> None: ...

class GetRunResponse(_message.Message):
    __slots__ = ("run_id", "events")
    RUN_ID_FIELD_NUMBER: _ClassVar[int]
    EVENTS_FIELD_NUMBER: _ClassVar[int]
    run_id: str
    events: _containers.RepeatedCompositeFieldContainer[SendMessageResponse]
    def __init__(self, run_id: _Optional[str] = ..., events: _Optional[_Iterable[_Union[SendMessageResponse, _Mapping]]] = ...) -> None: ...
