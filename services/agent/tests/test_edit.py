"""apply_edit: suggestions as CRDT ops from the run's own replica."""

from __future__ import annotations

import pytest
from support import NOTES, WORKSPACE, collect, finished, start

from skein_agent.app.tools.catalogue import build_catalogue
from skein_agent.app.tools.executor import ToolCall, ToolExecutor
from skein_agent.domain import crdt
from skein_agent.domain.events import Outcome
from skein_agent.fakes.infra import InMemoryCache
from skein_agent.fakes.model import call, say
from skein_agent.fakes.workspace import SERVER_REPLICA

EDIT = {
    "note_id": NOTES["raft"],
    "edits": [
        {"find": "consensous", "replace": "consensus"},
        {"find": "150 and 300", "replace": "150 to 300"},
    ],
}


async def test_edits_land_as_one_change_group_from_the_runs_registered_replica(make_harness, fx):
    before = fx.workspace.notes[NOTES["raft"]].text
    h = make_harness(
        [
            call("apply_edit", EDIT),
            call(
                "apply_edit",
                {"note_id": NOTES["fugue"], "edits": [{"find": "Fugue avoids", "replace": "Fugue prevents"}]},
            ),
            say("done"),
        ]
    )
    run = await start(h, "alice", "fix raft and fugue")
    events = await collect(h, run)
    assert finished(events).outcome is Outcome.COMPLETED
    note = fx.workspace.notes[NOTES["raft"]]
    assert note.text == before.replace("consensous", "consensus").replace("150 and 300", "150 to 300")
    note.seq.check_invariants()
    raft_group, fugue_group = fx.workspace.groups
    assert raft_group.note_id == NOTES["raft"] and raft_group.run_id == run.run_id
    assert raft_group.replica_id == fugue_group.replica_id != SERVER_REPLICA, "one agent replica per run"
    assert fx.workspace.replicas[raft_group.replica_id].run_id == run.run_id, "blame attributes it to the run"
    agent_ops = [op for op in note.seq.ops_since({}) if op.id.replica == raft_group.replica_id]
    assert len(agent_ops) == raft_group.op_count, "every op of the group came from that one replica"
    summary = next(e for e in events if e.kind == "tool_result")
    assert summary.summary == "Suggested 2 edits in 1 note"


async def test_a_second_replica_applying_the_same_ops_converges(make_harness, fx):
    """Another peer that receives the server's log reaches the same text."""
    h = make_harness([call("apply_edit", EDIT), say("ok")])
    await collect(h, await start(h, "alice", "fix"))
    server = fx.workspace.notes[NOTES["raft"]].seq
    peer = crdt.Sequence(424242)
    for op in crdt.decode_ops(crdt.encode_ops(server.ops_since({}))):
        peer.apply(op)
        peer.check_invariants()
    assert peer.text() == server.text()


@pytest.mark.parametrize(
    "edits,message",
    [
        ([{"find": "Raft", "replace": "RAFT"}], "occurs"),
        ([{"find": "not in the note", "replace": "x"}], "not found"),
        ([{"find": "a", "replace": "b", "extra": 1}], "only find, replace and occurrence"),
    ],
)
async def test_bad_edit_requests_return_errors_and_write_nothing(fx, edits, message):
    from support import NOTES as N
    from test_tools import ctx_for

    ex = ToolExecutor(build_catalogue(), InMemoryCache())
    out = await ex.execute(ToolCall("c", "apply_edit", {"note_id": N["raft"], "edits": edits}), ctx_for(fx))
    assert out.is_error and out.error_code == "invalid_argument" and message in out.content
    assert not fx.workspace.groups


async def test_viewer_edit_is_refused_by_documents_and_nothing_is_written(fx):
    from test_tools import ctx_for

    ex = ToolExecutor(build_catalogue(), InMemoryCache())
    out = await ex.execute(ToolCall("c", "apply_edit", EDIT), ctx_for(fx, "bob"))
    assert out.is_error and out.error_code == "permission_denied"
    assert not fx.workspace.groups
    assert "consensous" in fx.workspace.notes[NOTES["raft"]].text


async def test_edit_refuses_when_the_log_does_not_reproduce_the_text(fx):
    from test_tools import ctx_for

    note = fx.workspace.notes[NOTES["raft"]]
    real_text = type(note).text

    class Drifted(type(note)):
        @property
        def text(self) -> str:  # GetNote reports text the op log does not produce
            return real_text.fget(self) + " (edited elsewhere)"

    note.__class__ = Drifted
    ex = ToolExecutor(build_catalogue(), InMemoryCache())
    out = await ex.execute(ToolCall("c", "apply_edit", EDIT), ctx_for(fx))
    assert out.is_error and out.error_code == "failed_precondition"
    assert not fx.workspace.groups


async def test_editing_a_trashed_note_is_refused(fx):
    from test_tools import ctx_for

    fx.workspace.notes[NOTES["raft"]].trashed = True
    ex = ToolExecutor(build_catalogue(), InMemoryCache())
    out = await ex.execute(ToolCall("c", "apply_edit", EDIT), ctx_for(fx))
    assert out.is_error and "trash" in out.content


def test_workspace_constant_is_the_fixture_workspace():
    assert WORKSPACE == "ws-eval"
