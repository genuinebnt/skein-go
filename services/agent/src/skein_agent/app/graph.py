"""The agent's LangGraph graph.

```
START → model ──(tool calls)──→ gate ──→ tools ──→ model ...
          └──(no tool calls)──→ END       └──(declined)──→ END
```

- **model** calls the chat model with the system prompt, the conversation
  and the tool schemas. It streams text deltas to the run as they arrive,
  reserves a step from the budget first (raising BudgetExceededError when
  none is left), and records tokens and cost.
- **gate** looks at the tool calls that need confirmation (destructive or
  outward-facing). For each one it first checks the user may do it at all
  (no point asking otherwise), then pauses the graph with ``interrupt``.
  The run surfaces that as a ConfirmationRequest and resumes with the
  user's answer. Declining ends the run: no call from that batch executes.
  LangGraph re-executes an interrupted node from its start on resume, so the
  gate has no side effects, and each answer names the call it approves so a
  changed sequence of interrupts cannot apply one answer to another call.
- **tools** executes the calls in order. It refuses, by construction, any
  confirmation-requiring call without a recorded approval, whatever the
  graph's path: the gate is the only way in, and there is no setting to
  skip it.

Per-run objects (budget, delegated token, event sink) travel in LangGraph's
runtime ``context``, which is not checkpointed: only messages and decisions
are state.
"""

from __future__ import annotations

import json
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Annotated, Any, TypedDict

from langchain_core.language_models import BaseChatModel
from langchain_core.messages import AIMessage, AnyMessage, SystemMessage, ToolMessage
from langchain_core.runnables import Runnable
from langgraph.checkpoint.base import BaseCheckpointSaver
from langgraph.graph import END, START, StateGraph
from langgraph.graph.message import add_messages
from langgraph.graph.state import CompiledStateGraph
from langgraph.runtime import Runtime
from langgraph.types import Command, interrupt

from ..domain.budget import BudgetTracker
from ..domain.events import (
    ModelCallRecord,
    RunEvent,
    TextDelta,
    ToolCallFinished,
    ToolCallRecord,
    ToolCallStarted,
    TraceRecord,
)
from ..domain.ids import uuid7
from ..observability import tracer
from .tools.base import ToolRunContext
from .tools.executor import ToolCall, ToolExecutor, ToolOutput

APPROVED = "approved"
DECLINED = "declined"
DENIED = "denied"


class AgentState(TypedDict, total=False):
    """Checkpointed graph state."""

    messages: Annotated[list[AnyMessage], add_messages]
    # call_id -> {"status": approved|denied, ...}; set by gate, used by tools.
    decisions: dict[str, dict[str, Any]]
    # "declined" when the user declined a confirmation.
    stop: str


Emit = Callable[[RunEvent | TraceRecord], Awaitable[None]]


@dataclass
class RunContext:
    """Per-run objects the nodes need; LangGraph runtime context."""

    tools: ToolRunContext
    budget: BudgetTracker
    emit: Emit
    model: Runnable[Any, Any]
    system_prompt: str


def message_text(content: Any) -> str:
    """The visible text of a message or chunk: strings, or the ``text``
    blocks of a content list. Thinking and tool-use blocks are not text."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, str):
                parts.append(block)
            elif isinstance(block, dict) and block.get("type") == "text":
                parts.append(str(block.get("text", "")))
        return "".join(parts)
    return ""


def _tool_calls(message: AIMessage) -> list[dict[str, Any]]:
    """Valid tool calls, plus malformed ones as calls that will fail with a
    clear error (the provider still expects a result for each)."""
    calls = [dict(tc) for tc in message.tool_calls]
    for bad in message.invalid_tool_calls:
        calls.append(
            {
                "name": bad.get("name") or "",
                "id": bad.get("id"),
                "args": {"__invalid_json__": bad.get("error") or "unparseable"},
            }
        )
    for c in calls:
        c["id"] = c.get("id") or f"call_{uuid7()}"
    return calls


async def model_node(state: AgentState, runtime: Runtime[RunContext]) -> dict[str, Any]:
    """One model step."""
    ctx = runtime.context
    step = ctx.budget.begin_step()
    messages = [SystemMessage(ctx.system_prompt), *state["messages"]]
    started = time.monotonic()
    with tracer().start_as_current_span("agent.model", attributes={"step": step, "run_id": ctx.tools.run_id}):
        aggregate = None
        async for chunk in ctx.model.astream(messages):
            if text := message_text(chunk.content):
                await ctx.emit(TextDelta(text))
            aggregate = chunk if aggregate is None else aggregate + chunk
    if aggregate is None:
        raise RuntimeError("the model returned no output")
    usage = aggregate.usage_metadata or {}
    input_tokens, output_tokens = int(usage.get("input_tokens", 0)), int(usage.get("output_tokens", 0))
    cost = ctx.budget.record_model_call(input_tokens, output_tokens)
    await ctx.emit(ModelCallRecord(step, input_tokens, output_tokens, cost, (time.monotonic() - started) * 1000))
    message = AIMessage(
        content=aggregate.content,
        id=aggregate.id,
        tool_calls=[
            {"name": c["name"], "args": c["args"], "id": c["id"], "type": "tool_call"} for c in _tool_calls(aggregate)
        ],
        usage_metadata=aggregate.usage_metadata,
        response_metadata=aggregate.response_metadata,
    )
    for call in message.tool_calls:
        await ctx.emit(ToolCallStarted(call["id"], call["name"], json.dumps(call["args"], sort_keys=True)))
    return {"messages": [message]}


def route_after_model(state: AgentState) -> str:
    """Tool calls go through the gate; otherwise the run is done."""
    last = state["messages"][-1]
    return "gate" if isinstance(last, AIMessage) and last.tool_calls else END


def _as_call(tc: dict[str, Any]) -> ToolCall:
    return ToolCall(tc["id"], tc["name"], tc.get("args") or {})


def make_gate_node(executor: ToolExecutor) -> Callable[..., Awaitable[Command[str]]]:
    """Build the confirmation gate for this executor's tools."""

    async def gate_node(state: AgentState, runtime: Runtime[RunContext]) -> Command[str]:
        ctx = runtime.context
        last = state["messages"][-1]
        decisions: dict[str, dict[str, Any]] = {}
        for tc in last.tool_calls:
            if not executor.needs_confirmation(tc["name"]):
                continue
            call = _as_call(tc)
            denial = await executor.preflight(call, ctx.tools)
            if denial is not None:
                decisions[call.call_id] = {
                    "status": DENIED,
                    "content": denial.content,
                    "summary": denial.summary,
                    "error_code": denial.error_code,
                }
                continue
            answer = interrupt(
                {
                    "call_id": call.call_id,
                    "tool": call.name,
                    "arguments_json": json.dumps(dict(call.args), sort_keys=True),
                    "description": await executor.describe(call, ctx.tools),
                }
            )
            approved = (
                isinstance(answer, dict) and answer.get("call_id") == call.call_id and answer.get("approved") is True
            )
            if not approved:
                declined = [
                    ToolMessage(
                        content="The user declined this action. Nothing was done.", tool_call_id=t["id"], status="error"
                    )
                    for t in last.tool_calls
                ]
                return Command(goto=END, update={"decisions": {}, "stop": DECLINED, "messages": declined})
            decisions[call.call_id] = {"status": APPROVED}
        return Command(goto="tools", update={"decisions": decisions})

    return gate_node


def make_tools_node(executor: ToolExecutor) -> Callable[..., Awaitable[dict[str, Any]]]:
    """Build the node that executes tool calls."""

    async def tools_node(state: AgentState, runtime: Runtime[RunContext]) -> dict[str, Any]:
        ctx = runtime.context
        decisions = state.get("decisions") or {}
        results = []
        for tc in state["messages"][-1].tool_calls:
            call = _as_call(tc)
            decision = decisions.get(call.call_id, {})
            if decision.get("status") == DENIED:
                output = ToolOutput(
                    decision["content"],
                    decision["summary"],
                    True,
                    decision.get("error_code", ""),
                    trace_content=decision["content"],
                )
            elif executor.needs_confirmation(call.name) and decision.get("status") != APPROVED:
                output = ToolOutput(
                    "ERROR failed_precondition: this action was not confirmed by the user.",
                    "Not confirmed",
                    True,
                    "failed_precondition",
                )
            else:
                output = await executor.execute(call, ctx.tools)
            await ctx.emit(ToolCallFinished(call.call_id, output.summary, output.is_error))
            await ctx.emit(
                ToolCallRecord(
                    call.call_id,
                    call.name,
                    output.latency_ms,
                    output.error_code,
                    output.cached,
                    output.trace_content or output.content,
                )
            )
            results.append(
                ToolMessage(
                    content=output.content, tool_call_id=call.call_id, status="error" if output.is_error else "success"
                )
            )
        return {"messages": results, "decisions": {}}

    return tools_node


def build_graph(
    executor: ToolExecutor, checkpointer: BaseCheckpointSaver[Any] | None
) -> CompiledStateGraph[AgentState, RunContext, AgentState, AgentState]:
    """Compile the agent graph. One compiled graph serves every run; runs are
    separated by thread ID (the run ID) and by their runtime context."""
    g = StateGraph(AgentState, context_schema=RunContext)
    g.add_node("model", model_node)
    g.add_node("gate", make_gate_node(executor), destinations=("tools", END))
    g.add_node("tools", make_tools_node(executor))
    g.add_edge(START, "model")
    g.add_conditional_edges("model", route_after_model, ["gate", END])
    g.add_edge("tools", "model")
    return g.compile(checkpointer=checkpointer)


def bind_model(model: BaseChatModel, executor: ToolExecutor) -> Runnable[Any, Any]:
    """The chat model with the catalogue's tools bound."""
    return model.bind_tools(executor.model_schemas())
