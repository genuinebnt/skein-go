"""Adapters: caches, the event codec and consumer, logging, telemetry and config."""

from __future__ import annotations

import io
import json
import logging
import time

import pytest
from tests_support import make_app

from skein_ml.adapters.consumer import decode_or_skip, observed_handler
from skein_ml.adapters.events_codec import MalformedEvent, decode_event, encode_event
from skein_ml.adapters.grpc_stubs import events_pb2
from skein_ml.adapters.memory import InMemoryCache, InMemoryEventSource
from skein_ml.adapters.observability import (
    REDACTED,
    JsonFormatter,
    ServiceMetrics,
    request_context,
    setup_telemetry,
)
from skein_ml.app import events
from skein_ml.config import Config, ConfigError


class FakeClock:
    def __init__(self) -> None:
        self.now = 1000.0

    def __call__(self) -> float:
        return self.now


def test_in_memory_cache_expires_entries_after_their_ttl():
    clock = FakeClock()
    cache = InMemoryCache(clock)
    cache.set("k", b"v", ttl_seconds=10)
    clock.now += 9.9
    assert cache.get("k") == b"v"
    clock.now += 0.1
    assert cache.get("k") is None


def test_in_memory_cache_invalidates_every_key_of_a_tag():
    cache = InMemoryCache(FakeClock())
    cache.set("a", b"1", 60, ["workspace:1"])
    cache.set("b", b"2", 60, ["workspace:1", "model:x"])
    cache.set("c", b"3", 60, ["workspace:2"])
    assert cache.invalidate_tags(["workspace:1"]) == 2
    assert (cache.get("a"), cache.get("b"), cache.get("c")) == (None, None, b"3")
    assert cache.invalidate_tags(["model:x"]) == 0


def test_in_memory_cache_evicts_the_oldest_beyond_capacity():
    cache = InMemoryCache(FakeClock(), max_entries=2)
    for key in "abc":
        cache.set(key, key.encode(), 60, ["t"])
    assert cache.get("a") is None and len(cache) == 2
    assert cache.invalidate_tags(["t"]) == 2


@pytest.fixture
def redis_cache():
    fakeredis = pytest.importorskip("fakeredis")
    from skein_ml.adapters.redis_cache import RedisCache

    return RedisCache(fakeredis.FakeRedis())


def test_redis_cache_round_trips_and_expires(redis_cache):
    redis_cache.set("k", b"\x00bytes", ttl_seconds=0.05, tags=["t"])
    assert redis_cache.get("k") == b"\x00bytes"
    time.sleep(0.1)
    assert redis_cache.get("k") is None


def test_redis_cache_invalidates_tags_and_keeps_later_writes(redis_cache):
    redis_cache.set("a", b"1", 60, ["workspace:1"])
    redis_cache.set("b", b"2", 60, ["workspace:1"])
    redis_cache.set("c", b"3", 60, ["workspace:2"])
    assert redis_cache.invalidate_tags(["workspace:1", "missing"]) == 2
    assert redis_cache.get("a") is None and redis_cache.get("c") == b"3"
    redis_cache.set("a", b"new", 60, ["workspace:1"])
    assert redis_cache.get("a") == b"new"
    assert redis_cache.invalidate_tags(["workspace:1"]) == 1


def test_redis_errors_degrade_to_cache_misses():
    redis = pytest.importorskip("redis")
    from skein_ml.adapters.redis_cache import RedisCache

    class Broken:
        def __getattr__(self, name):
            def fail(*args, **kwargs):
                raise redis.ConnectionError("down")

            return fail

    cache = RedisCache(Broken())
    cache.set("k", b"v", 60, ["t"])
    assert cache.get("k") is None
    assert cache.invalidate_tags(["t"]) == 0


PAYLOADS = [
    events.NoteCreated("n1", "ws", "Title"),
    events.NoteRenamed("n1", "Renamed"),
    events.NoteTrashed("n1"),
    events.NoteRestored("n1"),
    events.NotePurged("n1"),
    events.BlocksChanged("n1", (events.ChangedBlock("r:1", "text", "code", "go"),), ("r:2",)),
    events.LinksChanged("n1", ("n2", "n3")),
]


@pytest.mark.parametrize("payload", PAYLOADS, ids=lambda p: p.kind)
def test_event_codec_round_trips_every_consumed_payload(payload):
    decoded = decode_event(encode_event("evt", payload, traceparent="00-abc-def-01"))
    assert decoded.id == "evt" and decoded.payload == payload
    assert decoded.traceparent == "00-abc-def-01"


def test_unconsumed_payloads_decode_as_ignored():
    message = events_pb2.Event(id="e")
    message.access_changed.workspace_id = "ws"
    assert isinstance(decode_event(message.SerializeToString()).payload, events.Ignored)


def test_malformed_bytes_are_rejected_and_skipped_by_the_consumer():
    with pytest.raises(MalformedEvent):
        decode_event(b"\xff\xff not protobuf")
    with pytest.raises(MalformedEvent):
        decode_event(events_pb2.Event().SerializeToString())  # no id
    assert decode_or_skip(b"\xff\xff") is None


def _metrics():
    from opentelemetry import metrics, trace

    return trace.get_tracer("test"), ServiceMetrics(metrics.get_meter("test"))


def test_consumer_applies_encoded_events_and_redelivers_failures():
    app = make_app(fail_first_embed=True)
    tracer, service_metrics = _metrics()
    source = InMemoryEventSource(decode=decode_or_skip)
    source.start(observed_handler(app, tracer, service_metrics))
    try:
        source.publish(encode_event("e1", events.NoteCreated("n", "ws", "t")))
        source.publish(
            encode_event(
                "e2", events.BlocksChanged("n", (events.ChangedBlock("b", "merkle trees"),))
            )
        )
        source.publish(b"\xff poison")
        source.publish(encode_event("e2", events.BlocksChanged("n", ())))  # duplicate id
        assert source.wait_idle(5.0)
    finally:
        source.stop(1.0)
    assert source.failures == 1
    assert [(h.note_id, h.block_id) for h in app.semantic_search("ws", "merkle", k=1).hits] == [
        ("n", "b")
    ]


def _format(record_extra: dict, **context) -> dict:
    formatter = JsonFormatter("ml", "1.2.3", "test")
    logger = logging.getLogger("format-test")
    record = logger.makeRecord(
        "format-test", logging.INFO, __file__, 1, "hello %s", ("you",), None, extra=record_extra
    )
    return json.loads(formatter.format(record))


def test_json_logs_carry_the_shared_fields_and_redact_secrets_and_contents():
    from opentelemetry.sdk.trace import TracerProvider

    tracer = TracerProvider().get_tracer("test")
    with tracer.start_as_current_span("span") as span, request_context("req-7"):
        line = _format(
            {
                "password": "hunter2",
                "text": "my private note",
                "input_tokens": 12,
                "nested": {"api_key": "k", "count": 3},
                "note_id": "n1",
            }
        )
        expected_trace = format(span.get_span_context().trace_id, "032x")
    assert line["msg"] == "hello you" and line["level"] == "INFO"
    assert (line["service"], line["version"], line["env"]) == ("ml", "1.2.3", "test")
    assert line["time"].endswith("Z")
    assert line["trace_id"] == expected_trace and len(line["span_id"]) == 16
    assert line["request_id"] == "req-7"
    assert line["password"] == REDACTED and line["text"] == REDACTED
    assert line["nested"] == {"api_key": REDACTED, "count": 3}
    assert line["input_tokens"] == 12 and line["note_id"] == "n1"


def test_logs_outside_a_span_or_request_have_no_ids():
    line = _format({})
    assert "trace_id" not in line and "request_id" not in line


def test_configure_logging_writes_json_lines():
    from skein_ml.adapters.observability import configure_logging

    stream = io.StringIO()
    root = logging.getLogger()
    saved = (list(root.handlers), root.level)
    try:
        configure_logging("ml", stream=stream)
        logging.getLogger("x").info("ready", extra={"port": 1})
    finally:
        root.handlers[:], root.level = saved[0], saved[1]
    assert json.loads(stream.getvalue())["port"] == 1


def test_telemetry_is_off_when_the_sdk_is_disabled():
    telemetry = setup_telemetry("ml", lookup={"OTEL_SDK_DISABLED": "true"}.get)
    assert not telemetry.enabled
    telemetry.shutdown()


def test_unsupported_otel_exporters_fail_fast():
    with pytest.raises(ValueError, match="OTEL_TRACES_EXPORTER"):
        setup_telemetry("ml", lookup={"OTEL_TRACES_EXPORTER": "zipkin"}.get)


def test_config_reads_defaults_and_validates():
    assert Config.from_env({}).address == "[::]:50051"
    config = Config.from_env({"SKEIN_ML_SUGGEST_THRESHOLD": "0.8", "SKEIN_ML_WORKERS": "2"})
    assert (config.suggest_threshold, config.workers) == (0.8, 2)
    with pytest.raises(ConfigError, match="SKEIN_ML_WORKERS"):
        Config.from_env({"SKEIN_ML_WORKERS": "many"})
    with pytest.raises(ConfigError, match="ONNX_MODEL_DIR"):
        Config.from_env({"SKEIN_ML_EMBEDDER": "onnx"})
