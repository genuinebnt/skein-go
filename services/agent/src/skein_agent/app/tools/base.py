"""Tool interface and the generic RPC-backed tool.

Most tools are ``RpcTool``: one RPC, a request built from the model's
arguments plus fields the agent fills in itself (the chat's workspace), and
a renderer that turns the response into text for the model and a short
summary for the user. Adding an RPC as a tool is one ``RpcTool(...)`` entry
in ``catalogue.py``; its schema comes from the request descriptor.

Arguments are parsed with ``json_format.ParseDict``, which rejects unknown
fields, wrong types and unknown enum names; the error goes back to the model
as a tool result so it can correct itself.
"""

from __future__ import annotations

import time
from collections.abc import Awaitable, Callable, Mapping
from dataclasses import dataclass, field
from typing import Any, Protocol

from google.protobuf import json_format
from google.protobuf.message import Message

from ...domain.errors import ErrorCode, ServiceError
from ...domain.policy import Effect
from ...domain.untrusted import SourceRegistry
from ..ports import Services
from .schema import message_schema, proto_comments


class ArgumentError(ValueError):
    """The model's arguments do not fit the tool's schema."""


@dataclass(slots=True)
class RunToolState:
    """Mutable per-run state some tools need (the agent's replica)."""

    replica_id: int = 0


@dataclass(slots=True)
class ToolRunContext:
    """Everything a tool call needs about the run it belongs to."""

    run_id: str
    user_id: str
    workspace_id: str
    token: str
    nonce: str
    sources: SourceRegistry
    services: Services
    deadline: float  # time.monotonic() value; every downstream call respects it
    call_timeout_s: float = 15.0
    max_result_chars: int = 24_000
    state: RunToolState = field(default_factory=RunToolState)

    def timeout(self) -> float:
        """Seconds left for one downstream call: the per-call limit, capped by
        the run's remaining wall-time budget."""
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise ServiceError(ErrorCode.DEADLINE_EXCEEDED, "the run's time budget is used up")
        return min(self.call_timeout_s, remaining)

    async def call(self, service: str, rpc: str, request: Message) -> Message:
        """Invoke a downstream RPC with the run's delegated token."""
        return await self.services.by_name(service).invoke(rpc, request, token=self.token, timeout_s=self.timeout())


@dataclass(frozen=True, slots=True)
class Rendered:
    """A tool's result. ``for_trace`` replaces ``content`` in the stored
    trace when the content holds a secret (a share-link token)."""

    content: str
    summary: str
    for_trace: str | None = None


class Tool(Protocol):
    """Something the model can call."""

    name: str
    description: str
    effect: Effect

    def schema(self) -> dict[str, Any]:
        """JSON Schema of the arguments."""
        ...

    def cache_key(self, args: Mapping[str, Any], ctx: ToolRunContext) -> bytes | None:
        """Bytes identifying the request for caching, or None if not cacheable."""
        ...

    async def run(
        self, args: Mapping[str, Any], ctx: ToolRunContext, cached: bytes | None
    ) -> tuple[Rendered, bytes | None]:
        """Execute (or render ``cached`` response bytes). Returns the result
        and response bytes worth caching, if any."""
        ...

    async def describe(self, args: Mapping[str, Any], ctx: ToolRunContext) -> str:
        """A plain description of what the call will do, for confirmation."""
        ...


Renderer = Callable[[Message, ToolRunContext], Rendered]
Describer = Callable[[Mapping[str, Any], ToolRunContext], Awaitable[str]]


@dataclass(frozen=True)
class Authorization:
    """An Authorize check to run before asking the user to confirm: no point
    asking about an action the user cannot take."""

    action: int  # identity_pb2.Action value
    resource_field: str


@dataclass
class RpcTool:
    """A tool that is exactly one RPC."""

    name: str
    service: str
    rpc: str
    request_type: type[Message]
    effect: Effect
    render: Renderer
    description: str = ""
    # Fields set by the agent, never by the model; values are taken from the
    # run context ("workspace_id") or are constants.
    from_context: tuple[str, ...] = ()
    constants: Mapping[str, Any] = field(default_factory=dict)
    hidden: tuple[str, ...] = ()
    required: tuple[str, ...] = ()
    describe_fn: Describer | None = None
    authorization: Authorization | None = None

    def __post_init__(self) -> None:
        if not self.description:
            full = f"skein.v1.{self.service_proto_name()}.{self.rpc}"
            self.description = proto_comments().get(full, f"Calls {self.rpc}.")

    def service_proto_name(self) -> str:
        """The proto service that defines this RPC, for comment lookup."""
        return {
            "documents": "DocumentService",
            "search": "SearchService",
            "graph": "GraphService",
            "ml": "MLService",
            "identity": "IdentityService",
            "notifications": "NotificationService",
        }[self.service]

    def schema(self) -> dict[str, Any]:
        """Schema of the request message minus agent-filled and hidden fields."""
        excluded = (*self.from_context, *self.constants, *self.hidden)
        return message_schema(self.request_type.DESCRIPTOR, exclude=excluded, required=self.required)

    def build_request(self, args: Mapping[str, Any], ctx: ToolRunContext) -> Message:
        """Parse the model's arguments into a request, then set agent fields."""
        allowed = set(self.schema()["properties"])
        unknown = set(args) - allowed
        if unknown:
            raise ArgumentError(f"unknown arguments: {', '.join(sorted(unknown))}")
        missing = [r for r in self.required if args.get(r) in (None, "")]
        if missing:
            raise ArgumentError(f"missing required arguments: {', '.join(missing)}")
        request = self.request_type()
        try:
            json_format.ParseDict(dict(args), request)
        except json_format.ParseError as err:
            raise ArgumentError(str(err)) from err
        for name in self.from_context:
            setattr(request, name, getattr(ctx, name))
        for name, value in self.constants.items():
            setattr(request, name, value)
        return request

    def cache_key(self, args: Mapping[str, Any], ctx: ToolRunContext) -> bytes | None:
        """The deterministic request bytes identify the call."""
        return self.build_request(args, ctx).SerializeToString(deterministic=True)

    async def run(
        self, args: Mapping[str, Any], ctx: ToolRunContext, cached: bytes | None
    ) -> tuple[Rendered, bytes | None]:
        """Call the RPC (or decode the cached response) and render it.

        The cache holds response bytes, not rendered text, because rendering
        assigns per-run source labels and nonces."""
        if cached is not None:
            response = _response_type(self).FromString(cached)
            return self.render(response, ctx), None
        response = await ctx.call(self.service, self.rpc, self.build_request(args, ctx))
        return self.render(response, ctx), response.SerializeToString(deterministic=True)

    async def describe(self, args: Mapping[str, Any], ctx: ToolRunContext) -> str:
        """Use the tool's describer, or a generic description."""
        if self.describe_fn is not None:
            return await self.describe_fn(args, ctx)
        return f"Run {self.name} with {json_format.MessageToJson(self.build_request(args, ctx), indent=None)}"


def _response_type(tool: RpcTool) -> type[Message]:
    from ... import pb

    module = {
        "documents": pb.documents_pb2,
        "search": pb.search_pb2,
        "graph": pb.search_pb2,
        "ml": pb.ml_pb2,
        "identity": pb.identity_pb2,
        "notifications": pb.notifications_pb2,
    }[tool.service]
    return getattr(module, f"{tool.rpc}Response")
