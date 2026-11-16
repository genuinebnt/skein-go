"""Paused runs are durable: they survive a restart, resume on Confirm in
another process, and every way out of a pause has exactly one winner.

A "restart" is a second agent over the same stores, checkpointer and
workspace (``Harness.restarted``). Each test runs on the in-memory stores
and, with ``-m integration``, on Postgres, through the ``storage`` fixture.
"""

from __future__ import annotations

import asyncio

import pytest
from support import NOTES, WORKSPACE, settings, start

from skein_agent.app.ports import RunInfo
from skein_agent.domain.errors import AgentError, AgentErrorKind
from skein_agent.domain.events import ConfirmationRequested, NoteRecord, Outcome, RunFinished, RunStatus
from skein_agent.fakes.model import call, say

DELETE_SCRATCH = call("delete_note", {"note_id": NOTES["scratch"]})


@pytest.fixture
def durable_harness(make_harness, storage):
    """A harness on ``storage``, so a restart keeps what storage keeps."""

    def make(script, **kwargs):
        return make_harness(
            script, run_store=storage.runs, chat_store=storage.chats, checkpointer=storage.checkpointer, **kwargs
        )

    return make


async def _pause(h, user: str = "alice", text: str = "delete scratch"):
    """Start a run and stream it to its first confirmation request."""
    run = await start(h, user, text)
    stream = run.stream()
    before = []
    async for event in stream:
        before.append(event)
        if isinstance(event, ConfirmationRequested):
            return run, stream, before, event
    raise AssertionError("the run never asked for confirmation")


async def _restart(h, stream):
    """Shut the first process down and start a second one."""
    await h.runs.shutdown()
    leftover = [e async for e in stream]
    assert not any(isinstance(e, RunFinished) for e in leftover), "shutdown does not finish a paused run"
    return h.restarted()


async def _finished(h, run_id: str, timeout_s: float = 10.0):
    async def poll():
        while True:
            trace = await h.run_store.get(run_id)
            if trace.status is RunStatus.FINISHED:
                return trace
            await asyncio.sleep(0.02)

    trace = await asyncio.wait_for(poll(), timeout_s)
    assert sum(isinstance(e, RunFinished) for e in trace.events()) == 1, "exactly one RunFinished"
    return trace


async def test_a_paused_run_survives_a_restart_and_resumes_where_it_stopped(durable_harness):
    h1 = durable_harness(
        [call("search", {"query": "Fugue interleaving"}), DELETE_SCRATCH, say("Fugue avoids interleaving [S1]. Done.")]
    )
    run, stream, before, request = await _pause(h1, text="fugue, then delete scratch")
    h2 = await _restart(h1, stream)
    assert (await h2.run_store.get(run.run_id)).status is RunStatus.PAUSED
    assert not h2.workspace.notes[NOTES["scratch"]].trashed

    await h2.runs.confirm(h2.principal("alice"), run.run_id, request.confirmation_id, True)
    trace = await _finished(h2, run.run_id)

    events = trace.events()
    assert events[: len(before)] == before, "the trace keeps what the first process streamed"
    done = events[-1]
    assert done.outcome is Outcome.COMPLETED and h2.workspace.notes[NOTES["scratch"]].trashed
    assert done.steps == 3, "the budget continued instead of starting over"
    assert trace.usage.steps == 3 and trace.usage.cost_usd > 0
    (citation,) = [e for e in events if e.kind == "citation"]
    assert citation.note_id == NOTES["fugue"], "a source label from before the restart still resolves"
    issued = [c for c in h2.workspace.calls if c.rpc == "IssueDelegatedToken"]
    assert len(issued) == 2 and all(c.run_id == "" for c in issued), "a fresh token from the confirming user"
    deletes = [c for c in h2.workspace.calls if c.rpc == "DeleteNote"]
    assert [c.run_id for c in deletes] == [run.run_id]
    chat = await h2.chat_store.get(run.chat_id)
    assert [t.role for t in chat.turns] == ["user", "assistant"]
    config = {"configurable": {"thread_id": run.run_id}}
    assert await h2.checkpointer.aget_tuple(config) is None, "a finished run leaves no checkpoints"


async def test_declining_after_a_restart_cancels_and_runs_nothing(durable_harness):
    h1 = durable_harness([DELETE_SCRATCH, say("unreachable")])
    run, stream, _, request = await _pause(h1)
    h2 = await _restart(h1, stream)
    await h2.runs.confirm(h2.principal("alice"), run.run_id, request.confirmation_id, False)
    trace = await _finished(h2, run.run_id)
    assert trace.events()[-1].outcome is Outcome.CANCELLED
    assert not h2.workspace.notes[NOTES["scratch"]].trashed


async def test_cancelling_after_a_restart_ends_the_run_without_resuming_it(durable_harness):
    h1 = durable_harness([DELETE_SCRATCH, say("unreachable")])
    run, stream, _, request = await _pause(h1)
    h2 = await _restart(h1, stream)
    with pytest.raises(AgentError) as stranger:
        await h2.runs.cancel(h2.principal("bob"), run.run_id)
    assert stranger.value.kind is AgentErrorKind.NOT_FOUND
    await h2.runs.cancel(h2.principal("alice"), run.run_id)
    trace = await _finished(h2, run.run_id)
    assert trace.events()[-1].outcome is Outcome.CANCELLED
    assert h2.model.remaining == 1, "the model was never called again"
    with pytest.raises(AgentError) as late:
        await h2.runs.confirm(h2.principal("alice"), run.run_id, request.confirmation_id, True)
    assert late.value.kind is AgentErrorKind.FAILED_PRECONDITION
    assert await h2.checkpointer.aget_tuple({"configurable": {"thread_id": run.run_id}}) is None


async def test_only_the_runs_user_can_confirm_it_after_a_restart(durable_harness):
    h1 = durable_harness([DELETE_SCRATCH, say("done")])
    run, stream, _, request = await _pause(h1)
    h2 = await _restart(h1, stream)
    with pytest.raises(AgentError) as err:
        await h2.runs.confirm(h2.principal("carol"), run.run_id, request.confirmation_id, True)
    assert err.value.kind is AgentErrorKind.NOT_FOUND
    with pytest.raises(AgentError) as wrong:
        await h2.runs.confirm(h2.principal("alice"), run.run_id, "not-the-id", True)
    assert wrong.value.kind is AgentErrorKind.FAILED_PRECONDITION
    assert (await h2.run_store.get(run.run_id)).status is RunStatus.PAUSED, "a refused answer leaves it paused"


async def test_a_paused_run_still_holds_its_chat_after_a_restart(durable_harness):
    h1 = durable_harness([DELETE_SCRATCH, say("done")])
    run, stream, _, _ = await _pause(h1)
    h2 = await _restart(h1, stream)
    with pytest.raises(AgentError) as err:
        await h2.runs.send_message(h2.principal("alice"), run.chat_id, "something else")
    assert err.value.kind is AgentErrorKind.FAILED_PRECONDITION


async def test_a_pause_past_its_timeout_cannot_be_confirmed_after_a_restart(durable_harness):
    h1 = durable_harness([DELETE_SCRATCH, say("done")], settings=settings(confirmation_timeout_s=0.2))
    run, stream, _, request = await _pause(h1)
    h2 = await _restart(h1, stream)
    await asyncio.sleep(0.3)
    with pytest.raises(AgentError, match="timed out") as err:
        await h2.runs.confirm(h2.principal("alice"), run.run_id, request.confirmation_id, True)
    assert err.value.kind is AgentErrorKind.FAILED_PRECONDITION
    trace = await _finished(h2, run.run_id)
    assert trace.events()[-1].outcome is Outcome.CANCELLED
    assert not h2.workspace.notes[NOTES["scratch"]].trashed


async def test_the_sweeper_ends_pauses_nobody_answered_after_a_restart(durable_harness):
    h1 = durable_harness([DELETE_SCRATCH, say("done")], settings=settings(confirmation_timeout_s=0.2))
    run, stream, _, _ = await _pause(h1)
    h2 = await _restart(h1, stream)
    assert await h2.runs.expire_stale() == 0, "not expired yet"
    await asyncio.sleep(0.3)
    assert await h2.runs.expire_stale() == 1
    assert await h2.runs.expire_stale() == 0, "ended once"
    trace = await _finished(h2, run.run_id)
    assert trace.events()[-1].outcome is Outcome.CANCELLED
    assert [r.text for r in trace.records() if isinstance(r, NoteRecord)] == ["confirmation timed out"]


async def test_a_run_left_running_by_a_stopped_process_is_failed_and_frees_its_chat(durable_harness):
    h = durable_harness([say("hello again")], settings=settings(max_wall_seconds=1))
    principal = h.principal("alice")
    chat_id = await h.chats.create(principal, WORKSPACE)
    # What a crash mid-segment leaves behind: a running run with no driver anywhere.
    assert await h.run_store.create(RunInfo("crashed-run", chat_id, "alice", WORKSPACE), at_ms=0)
    with pytest.raises(AgentError):
        await h.runs.send_message(principal, chat_id, "hello")
    assert await h.runs.expire_stale() == 1
    trace = await _finished(h, "crashed-run")
    assert trace.events()[-1].outcome is Outcome.FAILED
    assert any(isinstance(r, NoteRecord) and r.text.startswith("abandoned") for r in trace.records())
    run = await h.runs.send_message(principal, chat_id, "hello")
    assert (await _finished(h, run.run_id)).events()[-1].outcome is Outcome.COMPLETED


async def test_two_processes_answering_one_pause_have_exactly_one_winner(durable_harness):
    h1 = durable_harness([DELETE_SCRATCH, say("done")])
    run, _, _, request = await _pause(h1)
    h2 = h1.restarted()  # a second replica while the first is still alive
    results = await asyncio.gather(
        h1.runs.confirm(h1.principal("alice"), run.run_id, request.confirmation_id, True),
        h2.runs.confirm(h2.principal("alice"), run.run_id, request.confirmation_id, True),
        return_exceptions=True,
    )
    errors = [r for r in results if isinstance(r, AgentError)]
    assert len(errors) == 1 and errors[0].kind is AgentErrorKind.FAILED_PRECONDITION
    trace = await _finished(h1, run.run_id)
    assert trace.events()[-1].outcome is Outcome.COMPLETED
    assert sum(c.rpc == "DeleteNote" for c in h1.workspace.calls) == 1
    await h1.runs.shutdown()
    await h2.runs.shutdown()


async def test_the_original_driver_lets_go_when_another_process_answered_first(durable_harness):
    h1 = durable_harness([DELETE_SCRATCH, say("done")], settings=settings(confirmation_timeout_s=0.5))
    run, stream, _, request = await _pause(h1)
    h2 = h1.restarted()
    await h2.runs.confirm(h2.principal("alice"), run.run_id, request.confirmation_id, True)
    await _finished(h2, run.run_id)
    rest = await asyncio.wait_for(_drain(stream), 5)  # h1's own timeout fires, its claim fails
    assert not any(isinstance(e, RunFinished) for e in rest), "the loser does not finish the run"
    trace = await _finished(h1, run.run_id)
    assert trace.events()[-1].outcome is Outcome.COMPLETED


async def _drain(stream):
    return [e async for e in stream]
