"""Run behaviour: confirmation gate, budgets, cancellation, permissions,
delegated tokens, traces, rate limits."""

from __future__ import annotations

import asyncio
import json

import pytest
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage
from support import NOTES, WORKSPACE, collect, finished, settings, start

from skein_agent.app.graph import make_tools_node
from skein_agent.app.runs import model_calls
from skein_agent.domain.errors import AgentError, AgentErrorKind
from skein_agent.domain.events import (
    ConfirmationRequested,
    ModelCallRecord,
    NoteRecord,
    Outcome,
    TextDelta,
    ToolCallFinished,
    ToolCallRecord,
    ToolCallStarted,
)
from skein_agent.domain.ratelimit import RateLimit
from skein_agent.fakes.infra import FakeClock, InMemoryRateLimiter
from skein_agent.fakes.model import call, calls, say

DELETE_SCRATCH = call("delete_note", {"note_id": NOTES["scratch"]})


async def test_destructive_call_pauses_until_approved_then_runs(make_harness):
    h = make_harness([DELETE_SCRATCH, say("Done.")])
    run = await start(h, "alice", "delete my scratch note")
    paused: list[ConfirmationRequested] = []

    async def approve_after_checking(event):
        # While paused, nothing has been deleted and the run is waiting.
        assert not h.workspace.notes[NOTES["scratch"]].trashed
        assert run.pending is not None
        paused.append(event)
        await h.runs.confirm(h.principal("alice"), run.run_id, event.confirmation_id, True)

    events = []
    async for event in run.stream():
        events.append(event)
        if isinstance(event, ConfirmationRequested):
            await approve_after_checking(event)
    assert finished(events).outcome is Outcome.COMPLETED
    assert h.workspace.notes[NOTES["scratch"]].trashed
    (req,) = paused
    assert req.tool == "delete_note"
    assert json.loads(req.arguments_json) == {"note_id": NOTES["scratch"]}
    assert '"Scratch"' in req.description and "trash" in req.description
    kinds = [e.kind for e in events]
    assert kinds.index("confirmation") < kinds.index("tool_result")


async def test_declining_stops_the_run_and_runs_nothing_from_the_batch(make_harness):
    h = make_harness(
        [calls(("create_note", {"title": "x"}), ("delete_note", {"note_id": NOTES["scratch"]})), say("unreachable")]
    )
    run = await start(h, "alice", "make a note and delete scratch")
    events = await collect(h, run, answer=lambda e: False)
    assert finished(events).outcome is Outcome.CANCELLED
    assert not h.workspace.notes[NOTES["scratch"]].trashed
    assert not any(n.title == "x" for n in h.workspace.notes.values()), "no call of a declined batch runs"
    assert h.model.remaining == 1


async def test_several_confirmations_in_one_batch_are_asked_one_by_one(make_harness):
    h = make_harness(
        [
            calls(("delete_note", {"note_id": NOTES["scratch"]}), ("delete_note", {"note_id": NOTES["meeting"]})),
            say("Both deleted."),
        ]
    )
    run = await start(h, "alice", "delete scratch and the meeting notes")
    events = await collect(h, run, answer=lambda e: True)
    assert finished(events).outcome is Outcome.COMPLETED
    confirmations = [e for e in events if isinstance(e, ConfirmationRequested)]
    assert [json.loads(c.arguments_json)["note_id"] for c in confirmations] == [NOTES["scratch"], NOTES["meeting"]]
    assert len({c.confirmation_id for c in confirmations}) == 2
    assert h.workspace.notes[NOTES["scratch"]].trashed and h.workspace.notes[NOTES["meeting"]].trashed


async def test_confirm_with_a_wrong_id_is_refused_and_leaves_the_run_paused(make_harness):
    h = make_harness([DELETE_SCRATCH, say("Done.")])
    run = await start(h, "alice", "delete scratch")
    stream = run.stream()
    async for event in stream:
        if isinstance(event, ConfirmationRequested):
            break
    with pytest.raises(AgentError) as err:
        await h.runs.confirm(h.principal("alice"), run.run_id, "not-the-id", True)
    assert err.value.kind is AgentErrorKind.FAILED_PRECONDITION
    with pytest.raises(AgentError) as other:
        await h.runs.confirm(h.principal("carol"), run.run_id, event.confirmation_id, True)
    assert other.value.kind is AgentErrorKind.NOT_FOUND, "another user cannot answer"
    assert not h.workspace.notes[NOTES["scratch"]].trashed
    await h.runs.cancel(h.principal("alice"), run.run_id)
    rest = [e async for e in stream]
    assert finished(rest).outcome is Outcome.CANCELLED


async def test_unanswered_confirmation_times_out_as_cancelled(make_harness):
    h = make_harness([DELETE_SCRATCH, say("Done.")], settings=settings(confirmation_timeout_s=0.05))
    run = await start(h, "alice", "delete scratch")
    events = await collect(h, run, answer=lambda e: None)
    assert finished(events).outcome is Outcome.CANCELLED
    assert not h.workspace.notes[NOTES["scratch"]].trashed


async def test_tools_node_refuses_an_unconfirmed_destructive_call_whatever_the_path(make_harness):
    """Structural guarantee: even if the gate were bypassed, the tools node
    will not run a confirmation-requiring call without an approval."""
    h = make_harness([])
    node = make_tools_node(h.executor)
    message = AIMessage(
        content="", tool_calls=[{"name": "delete_note", "args": {"note_id": NOTES["scratch"]}, "id": "x"}]
    )

    class Runtime:
        class context:
            from skein_agent.app.tools.base import ToolRunContext
            from skein_agent.domain.untrusted import SourceRegistry

            tools = ToolRunContext(
                "r",
                "alice",
                WORKSPACE,
                h.tokens["alice"],
                "abc",
                SourceRegistry(),
                h.workspace.services(),
                deadline=1e18,
            )

            @staticmethod
            async def emit(item):
                return None

    out = await node({"messages": [HumanMessage("hi"), message], "decisions": {}}, Runtime())
    (result,) = out["messages"]
    assert isinstance(result, ToolMessage) and "not confirmed" in result.content
    assert not h.workspace.notes[NOTES["scratch"]].trashed


async def test_step_budget_ends_the_run_as_budget_exceeded(make_harness):
    h = make_harness([call("list_notes", {})], settings=settings(max_steps=3))
    h.model.repeat_last = True
    run = await start(h, "alice", "loop forever")
    events = await collect(h, run)
    done = finished(events)
    assert done.outcome is Outcome.BUDGET_EXCEEDED
    assert done.steps == 3
    trace = await h.run_store.get(run.run_id)
    assert model_calls(trace.records()) == 3
    assert any(isinstance(r, NoteRecord) and r.text == "budget exceeded: steps" for r in trace.records())


async def test_token_budget_stops_before_the_next_step(make_harness):
    h = make_harness([call("get_note", {"note_id": NOTES["raft"]})], settings=settings(max_tokens=600))
    h.model.repeat_last = True
    run = await start(h, "alice", "read raft again and again")
    done = finished(await collect(h, run))
    assert done.outcome is Outcome.BUDGET_EXCEEDED
    assert done.input_tokens + done.output_tokens >= 600
    trace = await h.run_store.get(run.run_id)
    assert any(r.text == "budget exceeded: tokens" for r in trace.records() if isinstance(r, NoteRecord))


async def test_cost_budget_stops_the_run(make_harness):
    from skein_agent.app.runs import RunSettings
    from skein_agent.domain.budget import Budget, Pricing

    s = RunSettings(budget=Budget(max_cost_usd=0.001), pricing=Pricing(1000.0, 1000.0))
    h = make_harness([call("list_notes", {})], settings=s)
    h.model.repeat_last = True
    run = await start(h, "alice", "spend")
    done = finished(await collect(h, run))
    assert done.outcome is Outcome.BUDGET_EXCEEDED and done.steps == 1


async def test_wall_time_budget_cancels_a_slow_tool_call(make_harness, fx):
    cancelled = asyncio.Event()

    async def slow(request):
        try:
            await asyncio.sleep(30)
        except asyncio.CancelledError:
            cancelled.set()
            raise

    fx.workspace.hooks[("search", "Search")] = slow
    h = make_harness([call("search", {"query": "raft"}), say("never")], settings=settings(max_wall_seconds=0.2))
    run = await start(h, "alice", "search slowly")
    done = finished(await collect(h, run, timeout_s=5))
    assert done.outcome is Outcome.BUDGET_EXCEEDED
    assert cancelled.is_set(), "the downstream call was cancelled, not left running"


async def test_waiting_for_confirmation_does_not_use_wall_time(make_harness):
    h = make_harness([DELETE_SCRATCH, say("Done.")], settings=settings(max_wall_seconds=0.5))
    run = await start(h, "alice", "delete scratch")

    async def answer_late(event):
        await asyncio.sleep(0.8)
        await h.runs.confirm(h.principal("alice"), run.run_id, event.confirmation_id, True)

    events = []
    async for event in run.stream():
        events.append(event)
        if isinstance(event, ConfirmationRequested):
            asyncio.get_running_loop().create_task(answer_late(event))
    assert finished(events).outcome is Outcome.COMPLETED


async def test_cancel_mid_tool_call_stops_promptly_and_cancels_the_downstream_call(make_harness, fx):
    entered, cancelled = asyncio.Event(), asyncio.Event()

    async def blocking(request):
        entered.set()
        try:
            await asyncio.sleep(60)
        except asyncio.CancelledError:
            cancelled.set()
            raise

    fx.workspace.hooks[("documents", "GetNote")] = blocking
    h = make_harness([call("get_note", {"note_id": NOTES["raft"]}), say("never")])
    run = await start(h, "alice", "read raft")
    await asyncio.wait_for(entered.wait(), 5)
    await h.runs.cancel(h.principal("alice"), run.run_id)
    events = await collect(h, run, timeout_s=2)
    assert finished(events).outcome is Outcome.CANCELLED
    assert cancelled.is_set()
    assert h.model.remaining == 1, "the model was not called again after cancelling"


async def test_cancel_while_awaiting_confirmation(make_harness):
    h = make_harness([DELETE_SCRATCH, say("Done.")])
    run = await start(h, "alice", "delete scratch")

    async def cancel_instead(event):
        await h.runs.cancel(h.principal("alice"), run.run_id)

    events = []
    async for event in run.stream():
        events.append(event)
        if isinstance(event, ConfirmationRequested):
            await cancel_instead(event)
    assert finished(events).outcome is Outcome.CANCELLED
    assert not h.workspace.notes[NOTES["scratch"]].trashed


async def test_cancelling_a_finished_run_is_a_noop_and_strangers_get_not_found(make_harness):
    h = make_harness([say("hi")])
    run = await start(h, "alice", "hello")
    await collect(h, run)
    await h.runs.cancel(h.principal("alice"), run.run_id)
    with pytest.raises(AgentError) as err:
        await h.runs.cancel(h.principal("bob"), run.run_id)
    assert err.value.kind is AgentErrorKind.NOT_FOUND


async def test_permission_error_becomes_a_tool_result_the_model_explains(make_harness):
    h = make_harness(
        [
            call("rename_note", {"note_id": NOTES["raft"], "title": "Mine now"}),
            lambda msgs: say(
                "You only have view access to that note."
                if "permission_denied" in str(msgs[-1].content)
                else "Renamed."
            ),
        ]
    )
    run = await start(h, "bob", "rename the raft note")
    events = await collect(h, run)
    assert finished(events).outcome is Outcome.COMPLETED
    (result,) = [e for e in events if isinstance(e, ToolCallFinished)]
    assert result.is_error and result.summary == "Not allowed"
    tool_message = h.model.seen[1][-1]
    assert isinstance(tool_message, ToolMessage)
    assert tool_message.content.startswith("ERROR permission_denied")
    assert "do not retry" in tool_message.content
    assert "view access" in "".join(e.text for e in events if isinstance(e, TextDelta))
    assert h.workspace.notes[NOTES["raft"]].title == "Raft consensus"


async def test_unreadable_notes_are_invisible_to_the_agent(make_harness):
    """bob can read only the two Raft notes; search through the agent shows
    exactly what bob could see himself."""
    h = make_harness([call("search", {"query": "Fugue interleaving"}), call("list_notes", {}), say("ok")])
    run = await start(h, "bob", "find fugue")
    await collect(h, run)
    search_result, list_result = (str(m[-1].content) for m in h.model.seen[1:3])
    assert "0 results" in search_result
    assert NOTES["fugue"] not in list_result
    assert NOTES["raft"] in list_result and NOTES["raft_log"] in list_result


async def test_destructive_call_the_user_cannot_make_is_refused_without_asking(make_harness):
    h = make_harness(
        [
            call("delete_note", {"note_id": NOTES["raft"]}),
            say("You can't delete that note: you only have view access."),
        ]
    )
    run = await start(h, "bob", "delete the raft note")
    events = await collect(h, run, answer=lambda e: pytest.fail("must not ask"))
    assert finished(events).outcome is Outcome.COMPLETED
    assert not [e for e in events if isinstance(e, ConfirmationRequested)]
    (result,) = [e for e in events if isinstance(e, ToolCallFinished)]
    assert result.is_error
    assert not h.workspace.notes[NOTES["raft"]].trashed


async def test_if_the_permission_check_fails_nothing_is_asked_or_run(make_harness, fx):
    from skein_agent.domain.errors import ErrorCode, ServiceError

    async def identity_down(request):
        raise ServiceError(ErrorCode.UNAVAILABLE, "identity is restarting")

    h = make_harness([DELETE_SCRATCH, say("Identity is unavailable; try again shortly.")])
    principal = h.principal("alice")
    chat_id = await h.chats.create(principal, WORKSPACE)
    fx.workspace.hooks[("identity", "Authorize")] = identity_down
    run = await h.runs.send_message(principal, chat_id, "delete scratch")
    events = await collect(h, run, answer=lambda e: pytest.fail("must not ask"))
    assert finished(events).outcome is Outcome.COMPLETED
    (result,) = [e for e in events if isinstance(e, ToolCallFinished)]
    assert result.is_error and result.summary == "Failed"
    assert "unavailable" in str(h.model.seen[1][-1].content)
    assert not h.workspace.notes[NOTES["scratch"]].trashed


async def test_every_downstream_call_carries_the_runs_delegated_token(make_harness):
    h = make_harness(
        [
            call("search", {"query": "raft"}),
            call("apply_edit", {"note_id": NOTES["raft"], "edits": [{"find": "consensous", "replace": "consensus"}]}),
            DELETE_SCRATCH,
            say("done"),
        ]
    )
    run = await start(h, "alice", "do things")
    await collect(h, run, answer=lambda e: True)
    calls_made = h.workspace.calls
    issue = [c for c in calls_made if c.rpc == "IssueDelegatedToken"]
    assert len(issue) == 1 and issue[0].run_id == "", "issued with the user's own token"
    after = calls_made[calls_made.index(issue[0]) + 1 :]
    assert after and all(c.user_id == "alice" and c.run_id == run.run_id for c in after)
    assert {"Search", "GetNote", "RegisterReplica", "ApplyOps", "DeleteNote", "Authorize"} <= {c.rpc for c in after}


async def test_a_user_without_agent_access_cannot_start_a_run(make_harness, fx):
    h = make_harness([say("hi")])
    principal = h.principal("alice")
    chat_id = await h.chats.create(principal, WORKSPACE)
    fx.workspace.add_user("mallory")
    h.tokens["mallory"] = "user-token-mallory"
    with pytest.raises(AgentError) as err:
        await h.chats.create(h.principal("mallory"), WORKSPACE)
    assert err.value.kind is AgentErrorKind.PERMISSION_DENIED
    with pytest.raises(AgentError) as forged:
        # mallory names alice's chat with her own token: not her chat.
        await h.runs.send_message(h.principal("mallory"), chat_id, "hi")
    assert forged.value.kind is AgentErrorKind.NOT_FOUND


async def test_rate_limit_refuses_runs_beyond_the_burst(make_harness):
    clock = FakeClock()
    limiter = InMemoryRateLimiter(RateLimit(rate=1, period_s=60, burst=1), clock)
    h = make_harness([say("one"), say("two"), say("three")], rate_limiter=limiter)
    principal = h.principal("alice")
    for _ in range(2):
        chat = await h.chats.create(principal, WORKSPACE)
        await collect(h, await h.runs.send_message(principal, chat, "hi"))
    chat = await h.chats.create(principal, WORKSPACE)
    with pytest.raises(AgentError) as err:
        await h.runs.send_message(principal, chat, "hi")
    assert err.value.kind is AgentErrorKind.RATE_LIMITED and err.value.retry_after_s > 0
    clock.advance(60)
    await collect(h, await h.runs.send_message(principal, chat, "hi"))


async def test_one_run_at_a_time_per_chat(make_harness):
    h = make_harness([DELETE_SCRATCH, say("done")])
    principal = h.principal("alice")
    chat = await h.chats.create(principal, WORKSPACE)
    run = await h.runs.send_message(principal, chat, "delete scratch")
    with pytest.raises(AgentError) as err:
        await h.runs.send_message(principal, chat, "and another thing")
    assert err.value.kind is AgentErrorKind.FAILED_PRECONDITION
    await h.runs.cancel(principal, run.run_id)
    await collect(h, run)


async def test_get_run_returns_the_streamed_events_and_trace_has_costs_and_timings(make_harness):
    h = make_harness([call("search", {"query": "Fugue interleaving"}), say("Fugue avoids interleaving [S1].")])
    run = await start(h, "alice", "what does fugue avoid")
    events = await collect(h, run)
    stored = await h.runs.get_run(h.principal("alice"), run.run_id)
    assert stored.events() == events
    done = finished(events)
    assert stored.usage.steps == done.steps and stored.usage.input_tokens == done.input_tokens
    assert stored.usage.cost_usd > 0, "cost is recorded with the run"
    with pytest.raises(AgentError):
        await h.runs.get_run(h.principal("bob"), run.run_id)
    trace = await h.run_store.get(run.run_id)
    records = trace.records()
    model = [r for r in records if isinstance(r, ModelCallRecord)]
    tools = [r for r in records if isinstance(r, ToolCallRecord)]
    assert [m.step for m in model] == [1, 2]
    assert all(m.input_tokens > 0 and m.cost_usd > 0 and m.latency_ms >= 0 for m in model)
    assert tools[0].tool == "search" and tools[0].error_code == "" and tools[0].latency_ms >= 0
    assert [r.role for r in records if r.kind == "message"] == ["user", "assistant"]
    assert [e.kind for e in events][:2] == ["tool_call", "tool_result"]
    started = next(e for e in events if isinstance(e, ToolCallStarted))
    assert json.loads(started.arguments_json) == {"query": "Fugue interleaving"}


async def test_citations_only_for_sources_the_run_retrieved(make_harness):
    h = make_harness([call("search", {"query": "Fugue interleaving"}), say("Yes [S1], also [S7] and [S1] again.")])
    run = await start(h, "alice", "fugue")
    events = await collect(h, run)
    cites = [e for e in events if e.kind == "citation"]
    assert len(cites) == 1 and cites[0].note_id == NOTES["fugue"] and cites[0].block_id
    assert cites[0].quote in h.workspace.notes[NOTES["fugue"]].text
    trace = await h.run_store.get(run.run_id)
    assert any(r.kind == "note" and "S7" in r.text for r in trace.records())


async def test_chat_history_carries_into_the_next_run(make_harness):
    h = make_harness([say("Fugue avoids interleaving."), say("As I said: interleaving.")])
    principal = h.principal("alice")
    chat = await h.chats.create(principal, WORKSPACE)
    await collect(h, await h.runs.send_message(principal, chat, "what does fugue avoid"))
    await collect(h, await h.runs.send_message(principal, chat, "say that again"))
    second = h.model.seen[1]
    texts = [m.content for m in second[1:]]
    assert texts == ["what does fugue avoid", "Fugue avoids interleaving.", "say that again"]
    chats, _ = await h.chats.list(principal, WORKSPACE, 0, "")
    assert chats[0].title == "what does fugue avoid"


async def test_a_model_failure_ends_the_run_as_failed_without_content_in_the_trace(make_harness):
    h = make_harness([])  # empty script: the first model call raises
    run = await start(h, "alice", "secret question about my diary")
    done = finished(await collect(h, run))
    assert done.outcome is Outcome.FAILED and done.steps == 1
    trace = await h.run_store.get(run.run_id)
    notes = [r.text for r in trace.records() if isinstance(r, NoteRecord)]
    assert notes == ["failed: ScriptExhaustedError"]


async def test_unknown_tool_and_bad_arguments_come_back_as_errors_the_model_can_fix(make_harness):
    h = make_harness(
        [
            calls(
                ("no_such_tool", {}),
                ("get_note", {"note_id": NOTES["raft"], "bogus": 1}),
                ("reading_plan", {"topic": "x", "minutes": "lots"}),
            ),
            say("fixed"),
        ]
    )
    run = await start(h, "alice", "try")
    events = await collect(h, run)
    results = [e for e in events if isinstance(e, ToolCallFinished)]
    assert [r.is_error for r in results] == [True, True, True]
    contents = [m.content for m in h.model.seen[1][-3:]]
    assert "no tool named" in contents[0]
    assert "unknown arguments: bogus" in contents[1]
    assert contents[2].startswith("ERROR invalid_argument")


async def test_shutdown_leaves_paused_runs_paused_and_cancels_working_ones(make_harness, fx):
    entered = asyncio.Event()

    async def blocking(request):
        entered.set()
        await asyncio.sleep(60)

    fx.workspace.hooks[("search", "Search")] = blocking  # not GetNote: the delete's description reads the note
    h = make_harness([DELETE_SCRATCH, call("search", {"query": "raft"}), say("never")])
    paused = await start(h, "alice", "delete scratch")
    paused_stream = paused.stream()
    async for event in paused_stream:
        if isinstance(event, ConfirmationRequested):
            break
    working = await start(h, "bob", "search raft")
    await asyncio.wait_for(entered.wait(), 5)
    await h.runs.shutdown()
    assert not any(e.kind == "finished" for e in [e async for e in paused_stream]), "the stream just ends"
    assert (await h.run_store.get(paused.run_id)).status.value == "paused"
    assert finished(await collect(h, working)).outcome is Outcome.CANCELLED


async def test_finished_runs_leave_no_checkpoints_behind(make_harness):
    h = make_harness([DELETE_SCRATCH, say("done")])
    saver = h.runs._checkpointer
    run = await start(h, "alice", "delete scratch")
    stream = run.stream()
    async for event in stream:
        if isinstance(event, ConfirmationRequested):
            assert run.run_id in saver.storage, "paused state is checkpointed"
            await h.runs.confirm(h.principal("alice"), run.run_id, event.confirmation_id, True)
    await run.driver
    assert run.run_id not in saver.storage
