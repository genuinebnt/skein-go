"""AgentService over grpc.aio: the inbound adapter.

Authentication: the gateway verifies the user's access token and forwards
two headers, ``x-skein-user-id`` and ``authorization`` (the user's own
bearer token). The agent trusts the first because only the gateway can
reach it (network policy), and uses the second only to ask identity for the
run's delegated token, so a forged user ID without that user's token gets
nowhere: identity refuses to delegate. Verifying the token's signature here
too, once identity publishes its key format, is a follow-up.

Each method sets request and user IDs for logs, continues the caller's
trace from ``traceparent``, and maps errors to status codes at this edge
only.

A client that disconnects from SendMessage does not cancel its run
(docs/api/websocket.md: only CancelRun does). The run goes on to completion
or to a confirmation pause, its events are kept in the RunStore, and the
client reads them with GetRun and answers a pause with Confirm, after a
disconnect as after a restart.

**Run usage in GetRun.** ``GetRunResponse`` has no field for cost, so GetRun
sends the run's usage as response header metadata ``x-skein-run-usage``:
JSON ``{"status", "steps", "input_tokens", "output_tokens", "cost_usd",
"model_calls": [{"step", "input_tokens", "output_tokens", "cost_usd",
"latency_ms"}]}``. grpc-gateway forwards it as the HTTP header
``Grpc-Metadata-X-Skein-Run-Usage``. Token totals are also in the events'
``RunFinished``. A ``RunUsage`` message in ``GetRunResponse`` would replace
the header (see the README's contract gaps).
"""

from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import AsyncIterator
from dataclasses import dataclass

import grpc
from google.protobuf.timestamp_pb2 import Timestamp
from opentelemetry import context as otel_context
from opentelemetry.propagate import extract

from .. import pb
from ..app.chats import Chats
from ..app.runs import AgentRuns, Principal
from ..domain.errors import AgentError, AgentErrorKind, ErrorCode, ServiceError
from ..domain.events import (
    CitationAdded,
    ConfirmationRequested,
    ModelCallRecord,
    RunEvent,
    RunFinished,
    RunTrace,
    TextDelta,
    ToolCallFinished,
    ToolCallStarted,
)
from ..domain.ids import uuid7
from ..observability import request_id_var, tracer, user_id_var

log = logging.getLogger(__name__)
a = pb.agent_pb2

_AGENT_CODES = {
    AgentErrorKind.UNAUTHENTICATED: grpc.StatusCode.UNAUTHENTICATED,
    AgentErrorKind.PERMISSION_DENIED: grpc.StatusCode.PERMISSION_DENIED,
    AgentErrorKind.NOT_FOUND: grpc.StatusCode.NOT_FOUND,
    AgentErrorKind.INVALID_ARGUMENT: grpc.StatusCode.INVALID_ARGUMENT,
    AgentErrorKind.FAILED_PRECONDITION: grpc.StatusCode.FAILED_PRECONDITION,
    AgentErrorKind.RATE_LIMITED: grpc.StatusCode.RESOURCE_EXHAUSTED,
}
_SERVICE_CODES = {
    ErrorCode.UNAVAILABLE: grpc.StatusCode.UNAVAILABLE,
    ErrorCode.DEADLINE_EXCEEDED: grpc.StatusCode.DEADLINE_EXCEEDED,
    ErrorCode.UNAUTHENTICATED: grpc.StatusCode.UNAUTHENTICATED,
    ErrorCode.PERMISSION_DENIED: grpc.StatusCode.PERMISSION_DENIED,
}


def event_to_proto(run_id: str, event: RunEvent) -> a.SendMessageResponse:
    """Map a domain event onto the stream message."""
    out = a.SendMessageResponse(run_id=run_id)
    match event:
        case TextDelta(text=text):
            out.text_delta = text
        case ToolCallStarted():
            out.tool_call.CopyFrom(
                a.ToolCall(call_id=event.call_id, tool=event.tool, arguments_json=event.arguments_json)
            )
        case ToolCallFinished():
            out.tool_result.CopyFrom(
                a.ToolResult(call_id=event.call_id, summary=event.summary, is_error=event.is_error)
            )
        case ConfirmationRequested():
            out.confirmation.CopyFrom(
                a.ConfirmationRequest(
                    confirmation_id=event.confirmation_id,
                    description=event.description,
                    tool=event.tool,
                    arguments_json=event.arguments_json,
                )
            )
        case CitationAdded():
            loc = pb.common_pb2.BlockRef(
                note_id=event.note_id, block_id=event.block_id, start=event.start, end=event.end
            )
            out.citation.CopyFrom(a.Citation(location=loc, quote=event.quote))
        case RunFinished():
            out.finished.CopyFrom(
                a.RunFinished(
                    outcome=int(event.outcome),
                    steps=event.steps,
                    input_tokens=event.input_tokens,
                    output_tokens=event.output_tokens,
                )
            )
    return out


RUN_USAGE_HEADER = "x-skein-run-usage"


def run_usage_json(trace: RunTrace) -> str:
    """The ``x-skein-run-usage`` value for a trace (see the module docs)."""
    usage = trace.usage_so_far()
    calls = [
        {
            "step": r.step,
            "input_tokens": r.input_tokens,
            "output_tokens": r.output_tokens,
            "cost_usd": round(r.cost_usd, 8),
            "latency_ms": round(r.latency_ms, 1),
        }
        for r in trace.records()
        if isinstance(r, ModelCallRecord)
    ]
    body = {
        "status": trace.status.value,
        "steps": usage.steps,
        "input_tokens": usage.input_tokens,
        "output_tokens": usage.output_tokens,
        "cost_usd": round(usage.cost_usd, 8),
        "model_calls": calls,
    }
    return json.dumps(body, separators=(",", ":"))


@dataclass(frozen=True)
class HeaderAuthenticator:
    """Reads the principal the gateway forwards (see module docs)."""

    user_header: str = "x-skein-user-id"

    def authenticate(self, metadata: dict[str, str]) -> Principal:
        """The caller, or AgentError(UNAUTHENTICATED) if either header is missing."""
        user_id = metadata.get(self.user_header, "")
        auth = metadata.get("authorization", "")
        token = auth[7:] if auth.lower().startswith("bearer ") else ""
        if not user_id or not token:
            raise AgentError(AgentErrorKind.UNAUTHENTICATED, "missing credentials")
        return Principal(user_id, token)


class _Call:
    """Per-RPC setup and teardown: IDs, trace context, principal."""

    def __init__(self, servicer: AgentServicer, context: grpc.aio.ServicerContext, method: str) -> None:
        self.md = {k.lower(): v for k, v in (context.invocation_metadata() or ()) if isinstance(v, str)}
        self.method = method
        self._servicer = servicer

    def __enter__(self) -> Principal:
        self._otel = otel_context.attach(extract(self.md))
        self._req = request_id_var.set(self.md.get("x-request-id") or uuid7())
        self._span_cm = tracer().start_as_current_span(f"skein.v1.AgentService/{self.method}")
        self._span_cm.__enter__()
        try:
            principal = self._servicer.auth.authenticate(self.md)
        except BaseException:
            # __exit__ does not run when __enter__ raises; undo by hand.
            self.__exit__(None, None, None)
            raise
        self._user = user_id_var.set(principal.user_id)
        return principal

    def __exit__(self, *exc: object) -> None:
        if hasattr(self, "_user"):
            user_id_var.reset(self._user)
        self._span_cm.__exit__(*exc)
        request_id_var.reset(self._req)
        otel_context.detach(self._otel)


async def _abort(context: grpc.aio.ServicerContext, err: Exception) -> None:
    if isinstance(err, AgentError):
        if err.kind is AgentErrorKind.RATE_LIMITED:
            context.set_trailing_metadata((("retry-after", str(max(1, round(err.retry_after_s)))),))
        await context.abort(_AGENT_CODES[err.kind], err.message)
    if isinstance(err, ServiceError):
        await context.abort(_SERVICE_CODES.get(err.code, grpc.StatusCode.INTERNAL), err.message)
    raise err


class AgentServicer(pb.agent_pb2_grpc.AgentServiceServicer):
    """AgentService implementation over the use cases."""

    def __init__(self, runs: AgentRuns, chats: Chats, auth: HeaderAuthenticator | None = None) -> None:
        self.runs = runs
        self.chats = chats
        self.auth = auth or HeaderAuthenticator()

    async def CreateChat(self, request: a.CreateChatRequest, context: grpc.aio.ServicerContext) -> a.CreateChatResponse:
        """The CreateChat RPC."""
        try:
            with _Call(self, context, "CreateChat") as principal:
                chat_id = await self.chats.create(principal, request.workspace_id, request.note_id)
                return a.CreateChatResponse(chat_id=chat_id)
        except (AgentError, ServiceError) as err:
            await _abort(context, err)
            raise

    async def ListChats(self, request: a.ListChatsRequest, context: grpc.aio.ServicerContext) -> a.ListChatsResponse:
        """The ListChats RPC."""
        try:
            with _Call(self, context, "ListChats") as principal:
                chats, nxt = await self.chats.list(
                    principal, request.workspace_id, request.page.page_size, request.page.page_token
                )
                out = a.ListChatsResponse(page=pb.common_pb2.PageResponse(next_page_token=nxt))
                for c in chats:
                    ts = Timestamp()
                    ts.FromMilliseconds(c.updated_at_ms)
                    out.chats.append(a.ChatSummary(chat_id=c.chat_id, title=c.title, updated_at=ts))
                return out
        except (AgentError, ServiceError) as err:
            await _abort(context, err)
            raise

    async def SendMessage(
        self, request: a.SendMessageRequest, context: grpc.aio.ServicerContext
    ) -> AsyncIterator[a.SendMessageResponse]:
        """The SendMessage RPC."""
        try:
            with _Call(self, context, "SendMessage") as principal:
                run = await self.runs.send_message(principal, request.chat_id, request.text)
                try:
                    async for event in run.stream():
                        yield event_to_proto(run.run_id, event)
                except asyncio.CancelledError:
                    log.info("client disconnected; the run continues", extra={"run_id": run.run_id})
                    raise
        except (AgentError, ServiceError) as err:
            await _abort(context, err)
            raise

    async def Confirm(self, request: a.ConfirmRequest, context: grpc.aio.ServicerContext) -> a.ConfirmResponse:
        """The Confirm RPC."""
        try:
            with _Call(self, context, "Confirm") as principal:
                await self.runs.confirm(principal, request.run_id, request.confirmation_id, request.approved)
                return a.ConfirmResponse()
        except (AgentError, ServiceError) as err:
            await _abort(context, err)
            raise

    async def CancelRun(self, request: a.CancelRunRequest, context: grpc.aio.ServicerContext) -> a.CancelRunResponse:
        """The CancelRun RPC."""
        try:
            with _Call(self, context, "CancelRun") as principal:
                await self.runs.cancel(principal, request.run_id)
                return a.CancelRunResponse()
        except (AgentError, ServiceError) as err:
            await _abort(context, err)
            raise

    async def GetRun(self, request: a.GetRunRequest, context: grpc.aio.ServicerContext) -> a.GetRunResponse:
        """The GetRun RPC."""
        try:
            with _Call(self, context, "GetRun") as principal:
                trace = await self.runs.get_run(principal, request.run_id)
                await context.send_initial_metadata(((RUN_USAGE_HEADER, run_usage_json(trace)),))
                return a.GetRunResponse(
                    run_id=request.run_id, events=[event_to_proto(request.run_id, e) for e in trace.events()]
                )
        except (AgentError, ServiceError) as err:
            await _abort(context, err)
            raise
