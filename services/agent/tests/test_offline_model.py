"""The offline model (``SKEIN_AGENT_MODEL_BACKEND=scripted``) through the
real run machinery: its ``/search`` and ``/delete`` commands drive the
tools, citations and the confirmation gate, so the service can be tested
end to end without an API key."""

from __future__ import annotations

from support import NOTES, collect, finished, start

from skein_agent.domain.events import CitationAdded, ConfirmationRequested, TextDelta, ToolCallStarted
from skein_agent.fakes.model import OFFLINE_ANSWER, offline_provider


def _text(events) -> str:
    return "".join(e.text for e in events if isinstance(e, TextDelta))


async def test_search_command_searches_and_cites_the_first_hit(make_harness):
    h = make_harness([], provider=offline_provider())
    run = await start(h, "alice", "/search Fugue interleaving")
    events = await collect(h, run, answer=lambda e: None)
    assert [e.tool for e in events if isinstance(e, ToolCallStarted)] == ["search"]
    citations = [e for e in events if isinstance(e, CitationAdded)]
    assert citations, "the answer cites a retrieved block"
    assert citations[0].note_id in NOTES.values()
    assert "[S1]" in _text(events)


async def test_delete_command_pauses_for_confirmation_and_declining_keeps_the_note(make_harness):
    h = make_harness([], provider=offline_provider())
    run = await start(h, "alice", f"/delete {NOTES['scratch']}")
    events = await collect(h, run, answer=lambda e: False)
    confirmations = [e for e in events if isinstance(e, ConfirmationRequested)]
    assert len(confirmations) == 1 and confirmations[0].tool == "delete_note"
    assert finished(events).outcome.name == "CANCELLED"
    assert not h.workspace.notes[NOTES["scratch"]].trashed


async def test_delete_command_approved_trashes_the_note(make_harness):
    h = make_harness([], provider=offline_provider())
    run = await start(h, "alice", f"/delete {NOTES['scratch']}")
    events = await collect(h, run, answer=lambda e: True)
    assert finished(events).outcome.name == "COMPLETED"
    assert h.workspace.notes[NOTES["scratch"]].trashed


async def test_any_other_message_gets_the_offline_answer_and_calls_nothing(make_harness):
    h = make_harness([], provider=offline_provider())
    run = await start(h, "alice", "please search for Fugue")
    events = await collect(h, run, answer=lambda e: None)
    assert not [e for e in events if isinstance(e, ToolCallStarted)]
    assert _text(events).strip() == OFFLINE_ANSWER
