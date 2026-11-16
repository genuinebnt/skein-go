"""A scripted chat model: deterministic, offline, no API key.

Each model call consumes the next script entry: either a fixed ``AIMessage``
or a function of the conversation so far, so a script can react to tool
results (read a note ID out of a search result, then call get_note). It
streams like a real provider: text in word-sized chunks, then a final chunk
carrying tool-call chunks and usage, which exercises the same aggregation
path as ChatAnthropic.

Token usage is estimated as characters / 4 so token and cost budgets have
something to count.
"""

from __future__ import annotations

import asyncio
import json
import re
from collections.abc import AsyncIterator, Callable, Iterator, Sequence
from typing import Any

from langchain_core.callbacks import AsyncCallbackManagerForLLMRun, CallbackManagerForLLMRun
from langchain_core.language_models import BaseChatModel
from langchain_core.messages import AIMessage, AIMessageChunk, BaseMessage, ToolMessage
from langchain_core.outputs import ChatGeneration, ChatGenerationChunk, ChatResult
from pydantic import PrivateAttr

Step = AIMessage | Callable[[list[BaseMessage]], AIMessage]


class ScriptExhaustedError(RuntimeError):
    """The model was called more times than the script has entries."""


def say(text: str) -> AIMessage:
    """A step that answers with text and no tool calls."""
    return AIMessage(content=text)


def call(name: str, args: dict[str, Any], text: str = "", call_id: str = "") -> AIMessage:
    """A step that calls one tool."""
    return AIMessage(
        content=text, tool_calls=[{"name": name, "args": args, "id": call_id or f"call_{name}", "type": "tool_call"}]
    )


def calls(*pairs: tuple[str, dict[str, Any]], text: str = "") -> AIMessage:
    """A step that calls several tools at once."""
    return AIMessage(
        content=text,
        tool_calls=[
            {"name": nm, "args": a, "id": f"call_{k}_{nm}", "type": "tool_call"} for k, (nm, a) in enumerate(pairs)
        ],
    )


def last_tool_text(messages: Sequence[BaseMessage]) -> str:
    """Content of the most recent tool result."""
    for msg in reversed(messages):
        if isinstance(msg, ToolMessage):
            return str(msg.content)
    return ""


def first_match(pattern: str, text: str) -> str:
    """First capture group of ``pattern`` in ``text`` (scripts use this to pick
    IDs and source labels out of tool results)."""
    found = re.search(pattern, text)
    if not found:
        raise ScriptExhaustedError(f"script expected {pattern!r} in a tool result")
    return found.group(1)


def _text_of(messages: Sequence[BaseMessage]) -> int:
    return sum(len(m.content) if isinstance(m.content, str) else len(json.dumps(m.content)) for m in messages)


class ScriptedChatModel(BaseChatModel):
    """A BaseChatModel that plays back a script."""

    script: list[Any]
    delay_s: float = 0.0
    # Replay the last entry forever instead of failing when the script ends
    # (the offline backend, and budget tests that loop until stopped).
    repeat_last: bool = False
    _position: int = PrivateAttr(default=0)
    _seen: list[list[BaseMessage]] = PrivateAttr(default_factory=list)
    _tools: list[Any] = PrivateAttr(default_factory=list)

    @property
    def _llm_type(self) -> str:
        return "scripted-fake"

    @property
    def seen(self) -> list[list[BaseMessage]]:
        """The messages each call received, in order."""
        return self._seen

    @property
    def bound_tools(self) -> list[Any]:
        """The tool definitions last bound."""
        return self._tools

    @property
    def remaining(self) -> int:
        """Script entries not yet played."""
        return len(self.script) - self._position

    def bind_tools(self, tools: Sequence[Any], **kwargs: Any) -> ScriptedChatModel:  # type: ignore[override]
        """Record the tool definitions; the script, not the tools, decides calls."""
        self._tools = list(tools)
        return self

    def _next(self, messages: list[BaseMessage]) -> AIMessage:
        self._seen.append(list(messages))
        if self._position >= len(self.script) and self.repeat_last and self.script:
            step = self.script[-1]
            return step(messages) if callable(step) else step
        if self._position >= len(self.script):
            raise ScriptExhaustedError(f"script has {len(self.script)} steps; call {self._position + 1} requested")
        step = self.script[self._position]
        self._position += 1
        return step(messages) if callable(step) else step

    def _usage(self, messages: list[BaseMessage], reply: AIMessage) -> dict[str, int]:
        inp = _text_of(messages) // 4 + 1
        out = len(str(reply.content)) // 4 + 20 * len(reply.tool_calls) + 1
        return {"input_tokens": inp, "output_tokens": out, "total_tokens": inp + out}

    def _generate(
        self,
        messages: list[BaseMessage],
        stop: list[str] | None = None,
        run_manager: CallbackManagerForLLMRun | None = None,
        **kwargs: Any,
    ) -> ChatResult:
        reply = self._next(messages)
        message = AIMessage(
            content=reply.content, tool_calls=reply.tool_calls, usage_metadata=self._usage(messages, reply)
        )
        return ChatResult(generations=[ChatGeneration(message=message)])

    def _stream(
        self,
        messages: list[BaseMessage],
        stop: list[str] | None = None,
        run_manager: CallbackManagerForLLMRun | None = None,
        **kwargs: Any,
    ) -> Iterator[ChatGenerationChunk]:
        reply = self._next(messages)
        yield from self._chunks(messages, reply)

    async def _astream(
        self,
        messages: list[BaseMessage],
        stop: list[str] | None = None,
        run_manager: AsyncCallbackManagerForLLMRun | None = None,
        **kwargs: Any,
    ) -> AsyncIterator[ChatGenerationChunk]:
        reply = self._next(messages)
        for chunk in self._chunks(messages, reply):
            if self.delay_s:
                await asyncio.sleep(self.delay_s)
            yield chunk

    def _chunks(self, messages: list[BaseMessage], reply: AIMessage) -> Iterator[ChatGenerationChunk]:
        text = str(reply.content)
        for piece in re.findall(r"\S+\s*|\s+", text):
            yield ChatGenerationChunk(message=AIMessageChunk(content=piece))
        tool_chunks = [
            {"name": tc["name"], "args": json.dumps(tc["args"]), "id": tc["id"], "index": k, "type": "tool_call_chunk"}
            for k, tc in enumerate(reply.tool_calls)
        ]
        yield ChatGenerationChunk(
            message=AIMessageChunk(
                content="", tool_call_chunks=tool_chunks, usage_metadata=self._usage(messages, reply)
            )
        )


class ScriptedModelProvider:
    """ModelProvider for a scripted model."""

    def __init__(self, model: ScriptedChatModel, name: str = "scripted-fake") -> None:
        self.model = model
        self._name = name

    @property
    def model_name(self) -> str:
        """See ModelProvider.model_name."""
        return self._name

    def chat_model(self) -> BaseChatModel:
        """See ModelProvider.chat_model."""
        return self.model


def offline_provider() -> ScriptedModelProvider:
    """A model for running the service without an API key: it answers every
    message by saying it is offline (container smoke tests)."""
    model = ScriptedChatModel(script=[say("The agent's model is not configured on this server.")], repeat_last=True)
    return ScriptedModelProvider(model, "offline")
