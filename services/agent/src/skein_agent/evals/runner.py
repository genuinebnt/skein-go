"""Runs eval tasks and scores them.

For each task: seed a fresh fixture, wire the agent (scripted model, or a
real provider), send the prompt, answer confirmations as the task says, and
evaluate the checks.

Metrics:

- **success**: the outcome is allowed and every applicable check passed.
- **citation faithfulness**: of all citation markers in answers, the share
  that named a source the run actually retrieved *and* whose quote occurs
  in the cited note's current text. Markers naming nothing retrieved count
  against it.
- **citation relevance**: of citations in tasks that list relevant notes,
  the share pointing at one of them.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass, field
from typing import Any

from ..app.ports import ModelProvider
from ..domain.events import ConfirmationRequested, RunFinished
from ..fakes.harness import build_harness
from . import fixture
from .tasks import RunResult, Task


@dataclass
class TaskReport:
    """The result of one task."""

    task_id: str
    category: str
    passed: bool
    outcome: str
    failed_checks: list[str]
    tools: list[str]
    steps: int
    input_tokens: int
    output_tokens: int
    seconds: float
    citations: int
    faithful_citations: int
    unknown_citations: int
    relevant_citations: int
    relevance_counted: int
    error: str = ""


@dataclass
class EvalReport:
    """All tasks plus aggregates."""

    mode: str
    model: str
    tasks: list[TaskReport] = field(default_factory=list)

    @property
    def passed(self) -> int:
        """Number of tasks that passed."""
        return sum(t.passed for t in self.tasks)

    @property
    def faithfulness(self) -> float:
        """Share of citation markers backed by a retrieved source and its text."""
        total = sum(t.citations + t.unknown_citations for t in self.tasks)
        return sum(t.faithful_citations for t in self.tasks) / total if total else 1.0

    @property
    def relevance(self) -> float:
        """Share of citations pointing at a task's relevant notes."""
        total = sum(t.relevance_counted for t in self.tasks)
        return sum(t.relevant_citations for t in self.tasks) / total if total else 1.0

    def as_dict(self) -> dict[str, Any]:
        """The report as JSON-ready data."""
        return {
            "mode": self.mode,
            "model": self.model,
            "passed": self.passed,
            "total": len(self.tasks),
            "citation_faithfulness": round(self.faithfulness, 3),
            "citation_relevance": round(self.relevance, 3),
            "tasks": [t.__dict__ for t in self.tasks],
        }


async def run_task(task: Task, *, mode: str, provider: ModelProvider | None, timeout_s: float) -> TaskReport:
    """Run one task and score it."""
    fx = fixture.seed()
    harness = build_harness(
        fx.workspace, fx.tokens, script=task.script if provider is None else None, provider=provider
    )
    principal = harness.principal(task.user)
    started = time.monotonic()
    events = []
    error = ""
    run_id = ""
    try:
        chat_id = await harness.chats.create(principal, fixture.WORKSPACE)
        run = await harness.runs.send_message(principal, chat_id, task.prompt)
        run_id = run.run_id

        async def consume() -> None:
            async for event in run.stream():
                events.append(event)
                if isinstance(event, ConfirmationRequested):
                    await harness.runs.confirm(
                        principal, run.run_id, event.confirmation_id, task.on_confirm == "approve"
                    )

        await asyncio.wait_for(consume(), timeout_s)
    except Exception as err:  # a crashed task is a failed task, not a crashed harness
        error = f"{type(err).__name__}: {err}"
    seconds = time.monotonic() - started
    trace = await harness.run_store.get(run_id) if run_id else None
    records = trace.records() if trace else []
    finished = events[-1] if events and isinstance(events[-1], RunFinished) else None
    if finished is None:
        return TaskReport(
            task.id,
            task.category,
            False,
            "none",
            ["run did not finish"],
            [],
            0,
            0,
            0,
            seconds,
            0,
            0,
            0,
            0,
            0,
            error or "no RunFinished",
        )
    result = RunResult(events, records, fx.workspace, run_id)
    checks = list(task.checks) + (task.scripted_checks if mode == "scripted" else task.live_checks)
    failed = []
    if finished.outcome not in task.outcomes:
        failed.append(f"outcome {finished.outcome.name} not in {sorted(o.name for o in task.outcomes)}")
    for name, check in checks:
        try:
            ok = check(result)
        except Exception as err:
            ok, name = False, f"{name} (raised {type(err).__name__})"
        if not ok:
            failed.append(name)
    faithful = sum(
        1
        for c in result.citations
        if c.note_id in fx.workspace.notes and c.quote and c.quote in fx.workspace.notes[c.note_id].text
    )
    relevant = sum(1 for c in result.citations if c.note_id in task.relevant_notes) if task.relevant_notes else 0
    return TaskReport(
        task.id,
        task.category,
        not failed and not error,
        finished.outcome.name,
        failed,
        result.tool_names(),
        finished.steps,
        finished.input_tokens,
        finished.output_tokens,
        round(seconds, 3),
        len(result.citations),
        faithful,
        result.unknown_citation_labels(),
        relevant,
        len(result.citations) if task.relevant_notes else 0,
        error,
    )


async def run_all(
    tasks: list[Task], *, mode: str, provider: ModelProvider | None, model: str, timeout_s: float
) -> EvalReport:
    """Run tasks one after another (keeps live runs within rate limits)."""
    report = EvalReport(mode, model)
    for task in tasks:
        report.tasks.append(await run_task(task, mode=mode, provider=provider, timeout_s=timeout_s))
    return report


def format_report(report: EvalReport) -> str:
    """A plain-text table."""
    lines = [f"mode={report.mode} model={report.model}", ""]
    width = max(len(t.task_id) for t in report.tasks)
    for t in report.tasks:
        mark = "PASS" if t.passed else "FAIL"
        lines.append(
            f"{mark}  {t.task_id:<{width}}  {t.outcome:<16} steps={t.steps:<2} tools={','.join(t.tools) or '-'}"
        )
        for f in t.failed_checks:
            lines.append(f"      - failed: {f}")
        if t.error:
            lines.append(f"      - error: {t.error}")
    lines += [
        "",
        f"success: {report.passed}/{len(report.tasks)}",
        f"citation faithfulness: {report.faithfulness:.2f}   citation relevance: {report.relevance:.2f}",
    ]
    return "\n".join(lines)
