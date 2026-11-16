"""The seed's agent evals speak this agent's tool vocabulary.

``tools/seed`` writes eval tasks (``truth/agent/evals.jsonl``) and a golden
transcript per eval (``chats.jsonl``) that name tools and their arguments.
Scoring the agent against them only works if those are the catalogue's
tools, so these tests read the committed small seed and check every name
against ``build_catalogue()`` and every golden call's arguments with the
tool's own parser. The seed generator has a matching Go test for the other
direction (transcripts obey their evals).
"""

from __future__ import annotations

import json
from collections.abc import Iterator
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

from skein_agent.app.tools.base import RpcTool
from skein_agent.app.tools.catalogue import build_catalogue
from skein_agent.app.tools.edit import ApplyEditTool
from skein_agent.domain.policy import requires_confirmation

SEED = Path(__file__).resolve().parents[3] / "testdata" / "seed" / "small"
TOOLS = {tool.name: tool for tool in build_catalogue()}


def _jsonl(rel: str) -> list[dict[str, Any]]:
    path = SEED / rel
    if not path.is_file():
        pytest.fail(f"{path} is missing; regenerate it with `make seed-small`")
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]


def _golden_calls() -> Iterator[tuple[str, str, dict[str, Any]]]:
    """(chat label, tool, arguments) for every tool call and confirmation."""
    for chat in _jsonl("chats.jsonl"):
        label = chat.get("eval_id") or chat["title"]
        for message in chat["messages"]:
            for event in message["events"]:
                if event["kind"] in ("tool_call", "confirmation"):
                    yield label, event["tool"], json.loads(event["arguments_json"])


def test_every_tool_an_eval_names_is_in_the_catalogue() -> None:
    evals = _jsonl("truth/agent/evals.jsonl")
    assert evals, "the seed has eval tasks"
    unknown = set()
    for e in evals:
        named = [*e["expected_tools"], *e["forbidden_tools"]]
        if e["confirmation"] is not None:
            named.append(e["confirmation"]["tool"])
        unknown |= {(e["id"], name) for name in named if name not in TOOLS}
    assert not unknown, f"eval tools missing from the catalogue: {sorted(unknown)}"


def test_confirmation_evals_name_tools_that_require_confirmation() -> None:
    for e in _jsonl("truth/agent/evals.jsonl"):
        if e["confirmation"] is not None:
            tool = TOOLS[e["confirmation"]["tool"]]
            assert requires_confirmation(tool.effect), f"{e['id']}: {tool.name} would not pause the run"


def test_every_golden_call_parses_with_its_tool() -> None:
    context = SimpleNamespace(workspace_id="ws")  # the only field build_request reads from the run
    calls = list(_golden_calls())
    assert calls, "the seed has golden transcripts with tool calls"
    for label, name, args in calls:
        tool = TOOLS.get(name)
        assert tool is not None, f"{label}: {name} is not in the catalogue"
        if isinstance(tool, ApplyEditTool):
            tool.parse(args)
        else:
            assert isinstance(tool, RpcTool)
            tool.build_request(args, context)  # raises ArgumentError on a bad argument
