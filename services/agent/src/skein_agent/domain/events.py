"""What a run emits, and what its trace records.

``RunEvent`` values are the stream the user sees (they map one to one onto
``SendMessageResponse`` in agent.proto). ``TraceEntry`` values are the
richer record kept for debugging and evaluation: every event plus model
calls with tokens, cost and latency, and tool calls with latency and error
codes.

Every type carries a ``kind`` tag so a Rust or JSON consumer can switch on it
without reflection.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass, field
from typing import Literal


class Outcome(enum.IntEnum):
    """How a run ended. Values equal RunFinished.Outcome in agent.proto."""

    UNSPECIFIED = 0
    COMPLETED = 1
    CANCELLED = 2
    BUDGET_EXCEEDED = 3
    FAILED = 4


@dataclass(frozen=True, slots=True)
class TextDelta:
    """A piece of the assistant's answer, in order."""

    text: str
    kind: Literal["text_delta"] = "text_delta"


@dataclass(frozen=True, slots=True)
class ToolCallStarted:
    """The model asked for a tool; arguments are JSON matching its schema."""

    call_id: str
    tool: str
    arguments_json: str
    kind: Literal["tool_call"] = "tool_call"


@dataclass(frozen=True, slots=True)
class ToolCallFinished:
    """A tool finished. ``summary`` is short and safe to show ("Read 3 notes")."""

    call_id: str
    summary: str
    is_error: bool
    kind: Literal["tool_result"] = "tool_result"


@dataclass(frozen=True, slots=True)
class ConfirmationRequested:
    """The run is paused until the user approves or declines this action."""

    confirmation_id: str
    description: str
    tool: str
    arguments_json: str
    kind: Literal["confirmation"] = "confirmation"


@dataclass(frozen=True, slots=True)
class CitationAdded:
    """The answer cites this location; ``quote`` is the retrieved text."""

    note_id: str
    block_id: str
    start: int
    end: int
    quote: str
    kind: Literal["citation"] = "citation"


@dataclass(frozen=True, slots=True)
class RunFinished:
    """Always the last event of a run."""

    outcome: Outcome
    steps: int
    input_tokens: int
    output_tokens: int
    kind: Literal["finished"] = "finished"


RunEvent = TextDelta | ToolCallStarted | ToolCallFinished | ConfirmationRequested | CitationAdded | RunFinished


@dataclass(frozen=True, slots=True)
class ModelCallRecord:
    """One model invocation: the unit the step budget counts."""

    step: int
    input_tokens: int
    output_tokens: int
    cost_usd: float
    latency_ms: float
    kind: Literal["model_call"] = "model_call"


@dataclass(frozen=True, slots=True)
class ToolCallRecord:
    """Timing and outcome of one tool execution.

    ``error_code`` is an ErrorCode value or empty. ``result`` is what the
    model saw, with secrets (share-link tokens) redacted.
    """

    call_id: str
    tool: str
    latency_ms: float
    error_code: str
    cached: bool
    result: str
    kind: Literal["tool_record"] = "tool_record"


@dataclass(frozen=True, slots=True)
class MessageRecord:
    """A user message or the assistant's final text."""

    role: Literal["user", "assistant"]
    text: str
    kind: Literal["message"] = "message"


@dataclass(frozen=True, slots=True)
class NoteRecord:
    """Something worth knowing about a run that is not an event, such as why
    it ended or a citation marker that named no retrieved source."""

    text: str
    kind: Literal["note"] = "note"


TraceRecord = ModelCallRecord | ToolCallRecord | MessageRecord | NoteRecord


@dataclass(frozen=True, slots=True)
class TraceEntry:
    """One timestamped line of a run's trace."""

    at_ms: float
    item: RunEvent | TraceRecord


@dataclass(slots=True)
class RunTrace:
    """Everything recorded for one run, in order."""

    run_id: str
    chat_id: str
    user_id: str
    entries: list[TraceEntry] = field(default_factory=list)

    def events(self) -> list[RunEvent]:
        """The user-visible events, as GetRun returns them."""
        return [e.item for e in self.entries if not isinstance(e.item, TraceRecord)]

    def records(self) -> list[TraceRecord]:
        """The trace-only records."""
        return [e.item for e in self.entries if isinstance(e.item, TraceRecord)]
