"""The RunStore and ChatStore contract, stated once and checked against the
in-memory stores and (``-m integration``) the Postgres ones, plus the JSON
codec the Postgres stores use."""

from __future__ import annotations

import asyncio

import pytest

from skein_agent.adapters.codec import item_from_json, item_to_json, paused_from_json, paused_to_json
from skein_agent.app.ports import Chat, ChatTurn, PausedRun, RunInfo
from skein_agent.domain.budget import RunUsage
from skein_agent.domain.events import (
    CitationAdded,
    ConfirmationRequested,
    MessageRecord,
    ModelCallRecord,
    NoteRecord,
    Outcome,
    RunFinished,
    RunStatus,
    TextDelta,
    ToolCallFinished,
    ToolCallRecord,
    ToolCallStarted,
    TraceEntry,
)
from skein_agent.domain.untrusted import Source

EVERY_KIND = [
    TextDelta("Hello"),
    ToolCallStarted("c1", "search", '{"query": "raft"}'),
    ToolCallFinished("c1", "Found 2 notes", False),
    ConfirmationRequested("k1", 'Move "Scratch" to the trash', "delete_note", '{"note_id": "n1"}'),
    CitationAdded("n1", "b1", 3, 9, "quoted"),
    RunFinished(Outcome.BUDGET_EXCEEDED, 4, 1200, 80),
    ModelCallRecord(1, 1000, 50, 0.0025, 812.5),
    ToolCallRecord("c1", "search", 12.0, "", True, "result text"),
    MessageRecord("assistant", "Done [S1]."),
    NoteRecord("confirmation timed out"),
]


def _run(run_id: str = "r1", chat_id: str = "chat-1") -> RunInfo:
    return RunInfo(run_id, chat_id, "alice", "ws-1")


def _paused(run: RunInfo, confirmation_id: str = "k1", at_ms: int = 1_000) -> PausedRun:
    return PausedRun(
        run=run,
        user_text="delete scratch",
        input_count=3,
        confirmation_id=confirmation_id,
        call_id="call-1",
        tool="delete_note",
        nonce="00ff",
        sources=(Source("S1", "n1", "b1", 0, 5, "hello"), Source("S2", "n2", "", 0, 0, "")),
        replica_id=7,
        usage=RunUsage(2, 1500, 90, 0.004),
        active_seconds=1.25,
        paused_at_ms=at_ms,
    )


@pytest.mark.parametrize("item", EVERY_KIND, ids=lambda i: i.kind)
def test_every_trace_item_round_trips_through_json(item) -> None:
    assert item_from_json(item_to_json(item)) == item


def test_paused_run_round_trips_through_json() -> None:
    paused = _paused(_run())
    assert paused_from_json(paused_to_json(paused)) == paused


def test_unknown_trace_kinds_are_refused_rather_than_guessed() -> None:
    with pytest.raises(ValueError, match="unknown trace item kind"):
        item_from_json({"kind": "telepathy"})


async def test_a_chat_has_at_most_one_unfinished_run(storage) -> None:
    runs = storage.runs
    assert await runs.create(_run("r1"), 0)
    assert not await runs.create(_run("r2"), 0), "second unfinished run in the chat"
    assert await runs.get("r2") is None, "a refused run leaves nothing behind"
    assert await runs.create(_run("r3", chat_id="chat-2"), 0), "other chats are independent"
    await runs.finish("r1", RunUsage())
    assert await runs.create(_run("r2"), 0)


async def test_trace_entries_come_back_in_order_with_status_and_usage(storage) -> None:
    runs = storage.runs
    await runs.create(_run(), 0)
    for i, item in enumerate(EVERY_KIND):
        await runs.append("r1", TraceEntry(float(i), item))
    trace = await runs.get("r1")
    assert [e.item for e in trace.entries] == EVERY_KIND
    assert (trace.status, trace.usage, trace.user_id, trace.chat_id) == (RunStatus.RUNNING, None, "alice", "chat-1")
    assert trace.usage_so_far() == RunUsage(1, 1000, 50, 0.0025), "derived from the model calls"
    await runs.finish("r1", RunUsage(3, 2000, 100, 0.01))
    trace = await runs.get("r1")
    assert trace.status is RunStatus.FINISHED and trace.usage == RunUsage(3, 2000, 100, 0.01)
    assert await runs.get("nope") is None


async def test_a_pause_is_claimed_once_and_only_for_its_confirmation(storage) -> None:
    runs = storage.runs
    run = _run()
    await runs.create(run, 0)
    assert await runs.pause(_paused(run))
    assert (await runs.get("r1")).status is RunStatus.PAUSED
    assert not await runs.pause(_paused(run)), "only a running run pauses"
    assert await runs.claim_paused("r1", "other", 5) is None
    claimed = await runs.claim_paused("r1", "k1", 5)
    assert claimed == _paused(run)
    assert (await runs.get("r1")).status is RunStatus.RUNNING
    assert await runs.claim_paused("r1", "k1", 6) is None, "a pause is claimed once"


async def test_concurrent_claims_of_one_pause_have_exactly_one_winner(storage) -> None:
    runs = storage.runs
    run = _run()
    await runs.create(run, 0)
    await runs.pause(_paused(run))
    results = await asyncio.gather(*(runs.claim_paused("r1", k, 5) for k in ("k1", "", "k1", "")))
    assert sum(r is not None for r in results) == 1


async def test_stale_finds_old_pauses_and_old_running_runs_and_claim_stale_wins_once(storage) -> None:
    runs = storage.runs
    paused_old, paused_new = _run("p-old", "c1"), _run("p-new", "c2")
    running_old, running_new = _run("r-old", "c3"), _run("r-new", "c4")
    for info, at in ((paused_old, 0), (paused_new, 0), (running_old, 100), (running_new, 900)):
        await runs.create(info, at)
    await runs.pause(_paused(paused_old, at_ms=100))
    await runs.pause(_paused(paused_new, at_ms=900))
    stale = await runs.stale(paused_before_ms=500, running_before_ms=500)
    assert sorted(stale) == ["p-old", "r-old"]
    assert not await runs.claim_stale("r-new", 500, 1000)
    assert not await runs.claim_stale("p-old", 500, 1000), "a paused run is not stale-running"
    assert await runs.claim_stale("r-old", 500, 1000)
    assert not await runs.claim_stale("r-old", 500, 1000), "the claim restarts its running clock"


async def test_chats_list_newest_first_and_page_after_a_chat(storage) -> None:
    chats = storage.chats
    for i, at in enumerate([300, 100, 300, 200]):
        await chats.create(Chat(f"c{i}", "alice", "ws", "", "", at))
    await chats.create(Chat("other-user", "bob", "ws", "", "", 999))
    await chats.create(Chat("other-ws", "alice", "ws-2", "", "", 999))
    everything = await chats.list("alice", "ws", limit=10, after="")
    assert [c.chat_id for c in everything] == ["c0", "c2", "c3", "c1"], "ties broken by ID"
    page = await chats.list("alice", "ws", limit=2, after="c2")
    assert [c.chat_id for c in page] == ["c3", "c1"]
    assert await chats.list("alice", "ws", limit=2, after="missing") == []


async def test_appending_turns_keeps_order_bumps_updated_and_titles_once(storage) -> None:
    chats = storage.chats
    await chats.create(Chat("c1", "alice", "ws", "note-1", "", 100))
    long_question = "What did we decide about compaction in the storage review " + "x" * 40
    await chats.append_turns("c1", [ChatTurn("user", long_question), ChatTurn("assistant", "A.")], 200)
    await chats.append_turns("c1", [ChatTurn("user", "Second question"), ChatTurn("assistant", "B.")], 300)
    chat = await chats.get("c1")
    assert [t.text for t in chat.turns] == [long_question, "A.", "Second question", "B."]
    assert chat.updated_at_ms == 300 and chat.note_id == "note-1"
    assert len(chat.title) == 60 and chat.title.endswith("…") and long_question.startswith(chat.title[:-1])
    await chats.append_turns("missing", [ChatTurn("user", "x")], 400)
    assert await chats.get("missing") is None
