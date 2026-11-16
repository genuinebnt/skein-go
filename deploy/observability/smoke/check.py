"""Checks a running local stack after emit.py has sent its telemetry.

Two contracts are checked, because both fail silently in production:

1. Every dashboard query returns data. The dashboards are written against the
   Prometheus names the collector produces from the names in
   docs/observability.md (for example rpc.server.call.duration becomes
   rpc_server_call_duration_seconds_bucket). A renamed metric or label turns
   a panel into "No data" without any error; this catches it.
2. Redaction works in all three backends: the credential-named attributes
   emit.py attached to a span, a span event, log records and a metric data
   point are gone, and the look-alike keys that must survive are still there.

It uses only the standard library so it runs without the smoke venv.
Usage: python check.py   (PROMETHEUS_URL, LOKI_URL and JAEGER_URL override
the localhost defaults). Exit status is non-zero on any failure.
"""

from __future__ import annotations

import json
import os
import re
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

from redaction_keys import KEPT_KEYS, REDACTED_KEYS, REDACTION_SERVICE

PROMETHEUS = os.environ.get("PROMETHEUS_URL", "http://localhost:9090")
LOKI = os.environ.get("LOKI_URL", "http://localhost:3100")
JAEGER = os.environ.get("JAEGER_URL", "http://localhost:16686")
DASHBOARDS = Path(__file__).resolve().parent.parent / "grafana" / "dashboards"

# Values Grafana would substitute. The ranges are wide enough to cover a
# one-minute emit run that finished a little while ago.
VARIABLES = {"$__rate_interval": "1m", "$__range": "10m"}


def get_json(url: str, params: dict[str, str]) -> dict:
    with urllib.request.urlopen(f"{url}?{urllib.parse.urlencode(params)}", timeout=15) as response:
        return json.load(response)


def rfc3339(seconds: float) -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(seconds))


def substitute(expr: str, variable_names: list[str]) -> str:
    for name, value in VARIABLES.items():
        expr = expr.replace(name, value)
    for name in variable_names:
        expr = expr.replace(f"${name}", ".+")
    return expr


def label_values_query(query: str) -> str:
    """label_values(selector, label) -> a PromQL query with the same series."""
    match = re.fullmatch(r"label_values\((.+),\s*(\w+)\)", query.strip())
    if not match:
        raise ValueError(f"unsupported variable query {query!r}")
    selector, label = match.groups()
    return f"count by ({label}) ({selector})"


def check_dashboards() -> list[str]:
    failures = []
    for path in sorted(DASHBOARDS.glob("*.json")):
        board = json.loads(path.read_text())
        names = [v["name"] for v in board["templating"]["list"]]
        queries = [
            (f"variable {v['name']}", "prometheus", label_values_query(v["query"]["query"]))
            for v in board["templating"]["list"]
        ]
        for panel in board["panels"]:
            for target in panel.get("targets", []):
                queries.append(
                    (f"panel {panel['title']!r} {target['refId']}", target["datasource"]["type"], target["expr"])
                )
        for where, kind, expr in queries:
            expr = substitute(expr, names)
            if kind == "prometheus":
                body = get_json(f"{PROMETHEUS}/api/v1/query", {"query": expr})
                ok = body["status"] == "success" and body["data"]["result"]
            else:
                now = time.time_ns()
                body = get_json(
                    f"{LOKI}/loki/api/v1/query_range",
                    {"query": expr, "start": str(now - 15 * 60 * 10**9), "end": str(now), "limit": "5"},
                )
                ok = body["status"] == "success" and body["data"]["result"]
            status = "ok  " if ok else "FAIL"
            print(f"{status} {path.name}: {where}")
            if not ok:
                failures.append(f"{path.name}: {where} returned no data: {expr}")
    return failures


def check_keys(signal: str, keys: set[str]) -> list[str]:
    failures = [
        f"{signal}: credential attribute {k!r} was not dropped"
        for k in REDACTED_KEYS
        if k in keys or k.replace(".", "_").replace("-", "_") in keys
    ]
    failures += [
        f"{signal}: attribute {k!r} was dropped but must be kept"
        for k in KEPT_KEYS
        if k not in keys and k.replace(".", "_") not in keys
    ]
    print(f"{'ok  ' if not failures else 'FAIL'} redaction in {signal}")
    return failures


def check_redaction() -> list[str]:
    failures = []
    # Jaeger's stable API v3 returns OTLP JSON, so keys are the original
    # dotted attribute names.
    window = {
        "query.service_name": REDACTION_SERVICE,
        "query.start_time_min": rfc3339(time.time() - 3600),
        "query.start_time_max": rfc3339(time.time() + 60),
    }
    resource_spans = get_json(f"{JAEGER}/api/v3/traces", window).get("result", {}).get("resourceSpans", [])
    if not resource_spans:
        return [f"traces: no trace for {REDACTION_SERVICE} in Jaeger"]
    spans = [span for scope in resource_spans[0]["scopeSpans"] for span in scope["spans"]]
    failures += check_keys("span attributes", {a["key"] for span in spans for a in span.get("attributes", [])})
    failures += check_keys(
        "span event attributes",
        {a["key"] for span in spans for event in span.get("events", []) for a in event.get("attributes", [])},
    )
    resource_keys = {a["key"] for a in resource_spans[0]["resource"]["attributes"]}
    if "deployment.environment" in resource_keys or "deployment.environment.name" not in resource_keys:
        failures.append("traces: deployment.environment was not renamed to deployment.environment.name")

    now = time.time_ns()
    streams = get_json(
        f"{LOKI}/loki/api/v1/query_range",
        {
            "query": f'{{service_name="{REDACTION_SERVICE}"}}',
            "start": str(now - 3600 * 10**9),
            "end": str(now),
            "limit": "10",
        },
    )["data"]["result"]
    if not streams:
        failures.append(f"logs: no log records for {REDACTION_SERVICE} in Loki")
    for stream in streams:
        failures += check_keys("log attributes", set(stream["stream"]))
        if "trace_id" not in stream["stream"]:
            failures.append("logs: trace_id is missing, so logs cannot link to traces")

    series = get_json(f"{PROMETHEUS}/api/v1/series", {"match[]": "skein_smoke_redaction_total"})["data"]
    if not series:
        failures.append("metrics: skein_smoke_redaction_total not found in Prometheus")
    for labels in series:
        failures += check_keys("metric attributes", set(labels))
    return failures


def main() -> int:
    failures = check_dashboards() + check_redaction()
    for failure in failures:
        print(f"FAIL {failure}", file=sys.stderr)
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
