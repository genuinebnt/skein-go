"""The Redis wire format shared with Go (docs/spec/events-redis.md)."""

from __future__ import annotations

import json

from conftest import REPO

from skein_events import Envelope
from skein_events.redis_backend import PARTITIONS, decode_fields, encode_fields, partition_for


def test_partition_matches_the_conformance_fixture():
    fixture = json.loads((REPO / "testdata/conformance/events/redis-wire.json").read_text())
    assert fixture["partitions"] == PARTITIONS
    for case in fixture["cases"]:
        assert partition_for(case["ordering_key"], case["id"]) == case["partition"], case


def test_fields_round_trip_as_bytes_the_way_redis_returns_them():
    e = Envelope(
        id="id-1",
        source="documents",
        payload=b"\x00\xff",
        ordering_key="n1",
        traceparent="00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
        attributes={"b": "2", "a": "1"},
    )
    fields = encode_fields(e)
    assert [k for k, _ in fields] == [
        "id",
        "source",
        "key",
        "traceparent",
        "attr:a",
        "attr:b",
        "payload",
    ]
    as_redis = {k.encode(): v if isinstance(v, bytes) else v.encode() for k, v in fields}
    assert decode_fields(as_redis) == e


def test_entry_fields_match_the_conformance_fixture():
    fx = json.loads((REPO / "testdata/conformance/events/redis-wire.json").read_text())["entry"]
    e = fx["envelope"]
    envelope = Envelope(
        id=e["id"],
        source=e["source"],
        ordering_key=e["ordering_key"],
        traceparent=e["traceparent"],
        attributes=e["attributes"],
        payload=e["payload_utf8"].encode(),
    )
    got = [[k, v.decode() if isinstance(v, bytes) else v] for k, v in encode_fields(envelope)]
    assert got == fx["fields"]
