"""JSON for what the Postgres stores keep: trace entries and paused runs.

Every trace item is a dataclass with a ``kind`` tag, so an item is stored as
its fields plus the tag and decoded by looking the tag up; a Rust or SQL
reader can switch on ``kind`` the same way. Enums are stored as their
values (``Outcome`` as the agent.proto number). Decoding an unknown kind
raises ``ValueError`` instead of guessing.
"""

from __future__ import annotations

import dataclasses
from typing import Any

from ..app.ports import PausedRun, RunInfo
from ..domain.budget import RunUsage
from ..domain.events import (
    CitationAdded,
    ConfirmationRequested,
    MessageRecord,
    ModelCallRecord,
    NoteRecord,
    Outcome,
    RunEvent,
    RunFinished,
    TextDelta,
    ToolCallFinished,
    ToolCallRecord,
    ToolCallStarted,
    TraceRecord,
)
from ..domain.untrusted import Source


def _kind(cls: type) -> str:
    # The field's default, not ``cls.kind``: with slots=True that is the slot.
    return next(f.default for f in dataclasses.fields(cls) if f.name == "kind")


_KINDS: dict[str, type] = {
    _kind(cls): cls
    for cls in (
        TextDelta,
        ToolCallStarted,
        ToolCallFinished,
        ConfirmationRequested,
        CitationAdded,
        RunFinished,
        ModelCallRecord,
        ToolCallRecord,
        MessageRecord,
        NoteRecord,
    )
}


def item_to_json(item: RunEvent | TraceRecord) -> dict[str, Any]:
    """A trace item as a JSON object with its ``kind``."""
    out = dataclasses.asdict(item)
    if isinstance(item, RunFinished):
        out["outcome"] = int(item.outcome)
    return out


def item_from_json(data: dict[str, Any]) -> RunEvent | TraceRecord:
    """The inverse of ``item_to_json``."""
    cls = _KINDS.get(data.get("kind", ""))
    if cls is None:
        raise ValueError(f"unknown trace item kind {data.get('kind')!r}")
    fields = {k: v for k, v in data.items() if k != "kind"}
    if cls is RunFinished:
        fields["outcome"] = Outcome(fields["outcome"])
    return cls(**fields)


def paused_to_json(paused: PausedRun) -> dict[str, Any]:
    """A paused run as a JSON object."""
    return dataclasses.asdict(paused)


def paused_from_json(data: dict[str, Any]) -> PausedRun:
    """The inverse of ``paused_to_json``."""
    fields = dict(data)
    fields["run"] = RunInfo(**fields["run"])
    fields["sources"] = tuple(Source(**s) for s in fields["sources"])
    fields["usage"] = RunUsage(**fields["usage"])
    return PausedRun(**fields)
