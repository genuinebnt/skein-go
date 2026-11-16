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

from .budget import RunUsage


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


class RunStatus(enum.Enum):
    """Where a run is in its life. A run moves running -> paused -> running
    any number of times, then to finished once; every move out of paused is a
    claim that exactly one process wins (see ``RunStore``)."""

    RUNNING = "running"
    PAUSED = "paused"
    FINISHED = "finished"


@dataclass(slots=True)
class RunTrace:
    """Everything recorded for one run, in order.

    ``usage`` is the total recorded when the run finished (None before);
    ``usage_so_far`` adds up the model calls in the trace, for runs still in
    progress.
    """

    run_id: str
    chat_id: str
    user_id: str
    entries: list[TraceEntry] = field(default_factory=list)
    status: RunStatus = RunStatus.RUNNING
    usage: RunUsage | None = None

    def usage_so_far(self) -> RunUsage:
        """Usage from the trace's model calls; equals ``usage`` once finished."""
        if self.usage is not None:
            return self.usage
        calls = [r for r in self.records() if isinstance(r, ModelCallRecord)]
        return RunUsage(
            steps=max((c.step for c in calls), default=0),
            input_tokens=sum(c.input_tokens for c in calls),
            output_tokens=sum(c.output_tokens for c in calls),
            cost_usd=sum(c.cost_usd for c in calls),
        )

    def events(self) -> list[RunEvent]:
        """The user-visible events, as GetRun returns them."""
        return [e.item for e in self.entries if not isinstance(e.item, TraceRecord)]

    def records(self) -> list[TraceRecord]:
        """The trace-only records."""
        return [e.item for e in self.entries if isinstance(e.item, TraceRecord)]
