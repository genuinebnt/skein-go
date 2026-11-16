"""``python -m skein_agent.evals [--model scripted|anthropic] [--json PATH] [--only ID]``

Scripted (the default) needs nothing and exits non-zero if any task fails.
Anthropic needs ``ANTHROPIC_API_KEY`` (model from ``SKEIN_AGENT_MODEL``);
without a key it prints that it skipped and exits 0. Live results are a
measurement, so they exit 0 unless ``--strict``.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys

from .runner import format_report, run_all
from .tasks import build_tasks


def main(argv: list[str] | None = None) -> int:
    """Command-line entry point; returns the exit code."""
    parser = argparse.ArgumentParser(prog="python -m skein_agent.evals")
    parser.add_argument("--model", choices=["scripted", "anthropic"], default="scripted")
    parser.add_argument("--json", dest="json_path", help="also write the report as JSON")
    parser.add_argument("--only", action="append", default=[], help="run only these task IDs")
    parser.add_argument("--strict", action="store_true", help="exit non-zero on any failure in live mode")
    args = parser.parse_args(argv)

    tasks = build_tasks()
    if args.only:
        tasks = [t for t in tasks if t.id in set(args.only)]
        if not tasks:
            print(f"no tasks match {args.only}", file=sys.stderr)
            return 2

    provider = None
    model_name = "scripted-fake"
    timeout = 15.0
    if args.model == "anthropic":
        key = os.environ.get("ANTHROPIC_API_KEY", "")
        if not key:
            print("skipped: ANTHROPIC_API_KEY is not set (live evals are optional)")
            return 0
        from ..adapters.anthropic_model import AnthropicModelProvider

        model_name = os.environ.get("SKEIN_AGENT_MODEL", "claude-sonnet-5-5")
        provider = AnthropicModelProvider(api_key=key, model=model_name)
        timeout = 180.0

    report = asyncio.run(run_all(tasks, mode=args.model, provider=provider, model=model_name, timeout_s=timeout))
    print(format_report(report))
    if args.json_path:
        with open(args.json_path, "w") as f:
            json.dump(report.as_dict(), f, indent=2, default=str)
    if args.model == "scripted" or args.strict:
        return 0 if report.passed == len(report.tasks) else 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
