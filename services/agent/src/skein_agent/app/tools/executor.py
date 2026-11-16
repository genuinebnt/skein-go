"""Runs tool calls: validation, caching, error mapping, timing.

Every failure a tool can meet becomes a *tool result* the model reads, not
an exception that ends the run. Permission errors in particular come back
with an instruction to explain them to the user and not to work around
them, which is how "the agent can never exceed its user's permissions"
(ADR-0009) shows up in conversation. Only cancellation and bugs escape.

Caching: results of READ tools are cached per user, keyed by the exact
request bytes, for a short TTL. Keys include a per-user *generation*
number that every successful write bumps, so a run never reads its own
stale data after creating, renaming or editing something. The user ID in
the key matters: two users asking the same question may be allowed to see
different notes.
"""

from __future__ import annotations

import base64
import hashlib
import json
import logging
import time
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

from ... import pb
from ...domain.errors import ErrorCode, ServiceError
from ...domain.policy import invalidates_cache, is_cacheable, requires_confirmation
from ...observability import Instruments, tracer
from ..ports import Cache
from .base import ArgumentError, RpcTool, Tool, ToolRunContext

log = logging.getLogger(__name__)

_GUIDANCE = {
    ErrorCode.PERMISSION_DENIED: (
        "The user does not have permission for this. Tell the user plainly what they lack; "
        "do not retry or try another way around it."
    ),
    ErrorCode.UNAUTHENTICATED: "The run's credentials were rejected. Tell the user to try again.",
    ErrorCode.NOT_FOUND: "It does not exist or the user cannot see it. Check the ID, or search for it.",
    ErrorCode.INVALID_ARGUMENT: "Fix the arguments and try again.",
    ErrorCode.FAILED_PRECONDITION: "The action is not possible in the current state.",
    ErrorCode.RESOURCE_EXHAUSTED: "A limit was reached. Tell the user to try later.",
    ErrorCode.UNAVAILABLE: "The service is temporarily unavailable. You may retry once.",
    ErrorCode.DEADLINE_EXCEEDED: "It took too long. Do not retry; tell the user.",
}


@dataclass(frozen=True, slots=True)
class ToolCall:
    """One call requested by the model."""

    call_id: str
    name: str
    args: Mapping[str, Any]


@dataclass(frozen=True, slots=True)
class ToolOutput:
    """What a call produced. ``content`` goes to the model, ``summary`` to the
    user, ``trace_content`` to the stored trace."""

    content: str
    summary: str
    is_error: bool
    error_code: str = ""
    cached: bool = False
    latency_ms: float = 0.0
    trace_content: str = ""


def _error(code: ErrorCode, message: str, summary: str) -> ToolOutput:
    content = f"ERROR {code.value}: {message}. {_GUIDANCE.get(code, '')}".strip()
    return ToolOutput(content, summary, True, code.value, trace_content=content)


def _error_summary(code: ErrorCode) -> str:
    return {
        ErrorCode.PERMISSION_DENIED: "Not allowed",
        ErrorCode.NOT_FOUND: "Not found",
        ErrorCode.INVALID_ARGUMENT: "Invalid request",
        ErrorCode.DEADLINE_EXCEEDED: "Timed out",
    }.get(code, "Failed")


class ToolExecutor:
    """Executes the catalogue's tools for runs."""

    def __init__(
        self,
        tools: list[Tool],
        cache: Cache,
        *,
        cache_ttl_s: float = 30.0,
        instruments: Instruments | None = None,
    ) -> None:
        names = [t.name for t in tools]
        if len(set(names)) != len(names):
            raise ValueError("tool names must be unique")
        self._tools = {t.name: t for t in tools}
        self._cache = cache
        self._ttl = cache_ttl_s
        self._instruments = instruments or Instruments.create()

    @property
    def tools(self) -> list[Tool]:
        """The tools, in catalogue order."""
        return list(self._tools.values())

    def get(self, name: str) -> Tool | None:
        """A tool by name."""
        return self._tools.get(name)

    def model_schemas(self) -> list[dict[str, Any]]:
        """Tool definitions in the provider-neutral function format LangChain
        converts for any chat model."""
        return [
            {"type": "function", "function": {"name": t.name, "description": t.description, "parameters": t.schema()}}
            for t in self._tools.values()
        ]

    def needs_confirmation(self, name: str) -> bool:
        """Whether calls to this tool pause for the user. Unknown tools do not
        (they fail without doing anything)."""
        tool = self._tools.get(name)
        return tool is not None and requires_confirmation(tool.effect)

    async def describe(self, call: ToolCall, ctx: ToolRunContext) -> str:
        """Confirmation text built from the arguments and live data, never from
        the model's prose, so an injected note cannot word the prompt."""
        tool = self._tools[call.name]
        try:
            return await tool.describe(call.args, ctx)
        except (ArgumentError, ServiceError):
            return f"Run {call.name} with {json.dumps(dict(call.args), sort_keys=True)}"

    async def preflight(self, call: ToolCall, ctx: ToolRunContext) -> ToolOutput | None:
        """Before asking the user to confirm, check the action is permitted at
        all. Returns an error output if it is not (or the arguments are bad),
        None if the user should be asked."""
        tool = self._tools.get(call.name)
        if tool is None:
            return _error(ErrorCode.INVALID_ARGUMENT, f"no tool named {call.name!r}", "Unknown tool")
        try:
            if isinstance(tool, RpcTool):
                tool.build_request(call.args, ctx)
                if tool.authorization is not None:
                    resource = str(call.args.get(tool.authorization.resource_field, ""))
                    allowed = await self._authorize(tool.authorization.action, resource, ctx)
                    if not allowed:
                        return _error(
                            ErrorCode.PERMISSION_DENIED, f"{call.name} is not allowed on {resource}", "Not allowed"
                        )
        except ArgumentError as err:
            return _error(ErrorCode.INVALID_ARGUMENT, str(err), "Invalid request")
        except ServiceError as err:
            return _error(err.code, err.message, _error_summary(err.code))
        return None

    async def _authorize(self, action: int, resource_id: str, ctx: ToolRunContext) -> bool:
        i = pb.identity_pb2
        req = i.AuthorizeRequest(
            subject=i.Subject(user_id=ctx.user_id, agent_run_id=ctx.run_id),
            checks=[i.Check(action=action, resource_id=resource_id)],
        )
        resp = await ctx.call("identity", "Authorize", req)
        return bool(resp.allowed) and all(resp.allowed)

    async def execute(self, call: ToolCall, ctx: ToolRunContext) -> ToolOutput:
        """Run one call and return its output. Raises only on cancellation or a bug."""
        started = time.monotonic()
        with tracer().start_as_current_span("agent.tool", attributes={"tool": call.name, "run_id": ctx.run_id}) as span:
            output = await self._execute(call, ctx)
            latency = (time.monotonic() - started) * 1000
            output = ToolOutput(
                output.content,
                output.summary,
                output.is_error,
                output.error_code,
                output.cached,
                latency,
                output.trace_content or output.content,
            )
            span.set_attribute("error_code", output.error_code)
            span.set_attribute("cached", output.cached)
        attrs = {"tool": call.name, "outcome": output.error_code or "ok"}
        self._instruments.tool_calls.add(1, attrs)
        self._instruments.tool_latency.record(latency, {"tool": call.name})
        log.info(
            "tool call finished",
            extra={
                "tool": call.name,
                "error_code": output.error_code,
                "latency_ms": round(latency, 1),
                "cached": output.cached,
            },
        )
        return output

    async def _execute(self, call: ToolCall, ctx: ToolRunContext) -> ToolOutput:
        tool = self._tools.get(call.name)
        if tool is None:
            return _error(ErrorCode.INVALID_ARGUMENT, f"no tool named {call.name!r}", "Unknown tool")
        try:
            key = await self._cache_key(tool, call, ctx)
            cached = await self._cache.get(key) if key else None
            rendered, to_cache = await tool.run(call.args, ctx, base64.b64decode(cached) if cached else None)
            if key and to_cache is not None:
                await self._cache.set(key, base64.b64encode(to_cache).decode(), self._ttl)
            if invalidates_cache(tool.effect):
                await self._cache.incr(_generation_key(ctx.user_id))
            return ToolOutput(
                rendered.content,
                rendered.summary,
                False,
                cached=cached is not None,
                trace_content=rendered.for_trace or rendered.content,
            )
        except ArgumentError as err:
            return _error(ErrorCode.INVALID_ARGUMENT, str(err), "Invalid request")
        except ServiceError as err:
            return _error(err.code, err.message, _error_summary(err.code))

    async def _cache_key(self, tool: Tool, call: ToolCall, ctx: ToolRunContext) -> str:
        if not is_cacheable(tool.effect):
            return ""
        request_bytes = tool.cache_key(call.args, ctx)
        if request_bytes is None:
            return ""
        generation = await self._cache.get(_generation_key(ctx.user_id)) or "0"
        digest = hashlib.sha256(request_bytes).hexdigest()
        return f"agent:v1:tool:{ctx.user_id}:{generation}:{tool.name}:{digest}"


def _generation_key(user_id: str) -> str:
    return f"agent:v1:gen:{user_id}"
