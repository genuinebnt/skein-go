"""Logs, spans and metrics: IDs present, content absent, telemetry off by default."""

from __future__ import annotations

import io
import json
import logging

import pytest
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import InMemoryMetricReader
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from support import NOTES, collect, start

from skein_agent.adapters.logging import JsonFormatter, log_context
from skein_agent.adapters.telemetry import configure_telemetry, telemetry_enabled
from skein_agent.fakes.model import call, say
from skein_agent.observability import Instruments


def test_json_log_lines_carry_ids_and_drop_content_fields() -> None:
    stream = io.StringIO()
    handler = logging.StreamHandler(stream)
    handler.setFormatter(JsonFormatter())
    logger = logging.getLogger("test.redaction")
    logger.addHandler(handler)
    logger.setLevel(logging.INFO)
    try:
        with log_context(request_id="req-9", user_id="alice", run_id="run-3"):
            logger.info(
                "tool call finished",
                extra={"tool": "get_note", "markdown": "SECRET NOTE TEXT", "token": "delegated-xyz", "latency_ms": 3.2},
            )
            try:
                raise ValueError("contains SECRET NOTE TEXT")
            except ValueError:
                logger.exception("run failed")
    finally:
        logger.removeHandler(handler)
    lines = [json.loads(line) for line in stream.getvalue().splitlines()]
    first = lines[0]
    assert (first["request_id"], first["user_id"], first["run_id"]) == ("req-9", "alice", "run-3")
    assert first["tool"] == "get_note" and first["latency_ms"] == 3.2
    assert "SECRET" not in stream.getvalue() and "delegated-xyz" not in stream.getvalue()
    assert lines[1]["exception_type"] == "ValueError"


def test_telemetry_is_disabled_without_an_endpoint_or_when_disabled() -> None:
    assert not telemetry_enabled({})
    assert not telemetry_enabled({"OTEL_SDK_DISABLED": "true", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://x:4317"})
    assert telemetry_enabled({"OTEL_EXPORTER_OTLP_ENDPOINT": "http://x:4317"})
    configure_telemetry({})()  # a no-op shutdown


async def test_runs_and_tool_calls_produce_spans_and_metrics(make_harness, monkeypatch) -> None:
    # Tests run with the SDK disabled; this one builds a local provider.
    monkeypatch.delenv("OTEL_SDK_DISABLED", raising=False)
    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    reader = InMemoryMetricReader()
    meter = MeterProvider(metric_readers=[reader]).get_meter("test")
    instruments = Instruments.create(meter)
    import skein_agent.app.graph as graph_mod
    import skein_agent.app.runs as runs_mod
    import skein_agent.app.tools.executor as exec_mod

    for mod in (graph_mod, runs_mod, exec_mod):
        monkeypatch.setattr(mod, "tracer", lambda: provider.get_tracer("test"))
    h = make_harness([call("get_note", {"note_id": NOTES["fugue"]}), say("ok")])
    h.runs._instruments = instruments
    h.executor._instruments = instruments
    run = await start(h, "alice", "read fugue")
    await collect(h, run)
    await run.driver  # the run span closes after RunFinished is published

    spans = {s.name: s for s in exporter.get_finished_spans()}
    assert {"agent.run", "agent.model", "agent.tool"} <= set(spans)
    assert spans["agent.run"].attributes["outcome"] == "COMPLETED"
    assert spans["agent.tool"].attributes["tool"] == "get_note"
    assert spans["agent.tool"].context.trace_id == spans["agent.run"].context.trace_id, "one trace per run"
    exported = json.dumps([s.to_json() for s in exporter.get_finished_spans()])
    assert "Fugue avoids" not in exported, "no note text in spans"

    data = reader.get_metrics_data()
    names = {m.name for rm in data.resource_metrics for sm in rm.scope_metrics for m in sm.metrics}
    assert {
        "agent.runs",
        "agent.run.steps",
        "agent.tokens",
        "agent.run.duration",
        "agent.tool.calls",
        "agent.tool.duration",
        "agent.cost",
    } <= names


@pytest.mark.parametrize("missing", ["ANTHROPIC_API_KEY"])
def test_config_requires_a_key_for_the_anthropic_backend(missing) -> None:
    from skein_agent.config import ConfigError, load_settings

    with pytest.raises(ConfigError, match=missing):
        load_settings({})
    s = load_settings({"SKEIN_AGENT_MODEL_BACKEND": "scripted"})
    assert s.model == "claude-sonnet-5-5" and s.pricing.input_per_mtok == 2.0


def test_config_rejects_unknown_models_without_prices_and_bad_numbers() -> None:
    from skein_agent.config import ConfigError, load_settings

    base = {"ANTHROPIC_API_KEY": "k"}
    with pytest.raises(ConfigError, match="no price known"):
        load_settings({**base, "SKEIN_AGENT_MODEL": "claude-future-9"})
    s = load_settings(
        {
            **base,
            "SKEIN_AGENT_MODEL": "claude-future-9",
            "SKEIN_AGENT_PRICE_INPUT_PER_MTOK": "1",
            "SKEIN_AGENT_PRICE_OUTPUT_PER_MTOK": "5",
        }
    )
    assert (s.pricing.input_per_mtok, s.pricing.output_per_mtok) == (1.0, 5.0)
    with pytest.raises(ConfigError, match="not a number"):
        load_settings({**base, "SKEIN_AGENT_MAX_STEPS": "many"})
    with pytest.raises(ConfigError):
        load_settings({**base, "SKEIN_AGENT_MAX_STEPS": "0"})
    assert "sk-" not in repr(load_settings({"ANTHROPIC_API_KEY": "sk-secret"}))


def test_anthropic_provider_builds_a_streaming_chat_model_without_network() -> None:
    from skein_agent.adapters.anthropic_model import AnthropicModelProvider

    model = AnthropicModelProvider(api_key="sk-test", model="claude-sonnet-5-5", max_tokens=1234).chat_model()
    assert model.model == "claude-sonnet-5-5" and model.max_tokens == 1234 and model.streaming
