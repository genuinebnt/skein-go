"""The eval harness runs offline with the scripted model; live runs are
optional and skipped without a key."""

from __future__ import annotations

import json
import os

import pytest

from skein_agent.evals.__main__ import main
from skein_agent.evals.runner import run_all
from skein_agent.evals.tasks import build_tasks


def test_there_are_8_to_12_tasks_covering_every_required_category() -> None:
    tasks = build_tasks()
    assert 8 <= len(tasks) <= 12
    assert {"factual", "multi_hop", "edit", "confirmation", "permission", "injection"} <= {t.category for t in tasks}
    assert len({t.id for t in tasks}) == len(tasks)


async def test_every_task_passes_with_the_scripted_model() -> None:
    report = await run_all(build_tasks(), mode="scripted", provider=None, model="scripted-fake", timeout_s=10)
    failures = {t.task_id: t.failed_checks or t.error for t in report.tasks if not t.passed}
    assert not failures
    assert report.faithfulness == 1.0 and report.relevance == 1.0


def test_cli_exits_zero_and_writes_json(tmp_path, capsys) -> None:
    out = tmp_path / "report.json"
    assert main(["--json", str(out), "--only", "factual-fugue", "--only", "delete-declined"]) == 0
    data = json.loads(out.read_text())
    assert data["passed"] == data["total"] == 2
    assert "success: 2/2" in capsys.readouterr().out


def test_cli_skips_live_mode_without_a_key(monkeypatch, capsys) -> None:
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    assert main(["--model", "anthropic"]) == 0
    assert "skipped" in capsys.readouterr().out


@pytest.mark.live_model
@pytest.mark.skipif(not os.environ.get("ANTHROPIC_API_KEY"), reason="needs ANTHROPIC_API_KEY")
def test_live_evals_run_against_the_real_model(capsys) -> None:
    main(["--model", "anthropic"])
    assert "success:" in capsys.readouterr().out
