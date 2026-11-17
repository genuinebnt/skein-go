from __future__ import annotations

import pytest

from skein_events import EventsConfig, MemoryBroker, open_broker


def test_default_is_pubsub_and_disabled_without_a_project():
    config = EventsConfig.from_env({})
    assert config.backend == "pubsub" and not config.enabled


def test_redis_needs_no_google_variable_and_falls_back_to_the_services_url():
    config = EventsConfig.from_env({"EVENTS_BACKEND": "redis"}, redis_url="redis://h:1/3")
    assert (config.backend, config.redis_url, config.redis_db, config.enabled) == (
        "redis",
        "redis://h:1/3",
        0,
        True,
    )
    config = EventsConfig.from_env(
        {"EVENTS_BACKEND": "redis", "REDIS_URL": "redis://a", "EVENTS_REDIS_URL": "redis://b"}
    )
    assert config.redis_url == "redis://b"
    config = EventsConfig.from_env({"PUBSUB_PROJECT": "generic"}, pubsub_project="skein-local")
    assert config.pubsub_project == "skein-local" and config.enabled


@pytest.mark.parametrize(
    "env",
    [
        {"EVENTS_BACKEND": "kafka"},
        {"EVENTS_BACKEND": "redis"},
        {"EVENTS_BACKEND": "memory", "EVENTS_REDIS_DB": "x"},
    ],
)
def test_rejects_bad_values_naming_the_variable(env):
    with pytest.raises(ValueError, match="EVENTS_"):
        EventsConfig.from_env(env)


def test_open_memory_provisions_the_default_topology():
    broker = open_broker(EventsConfig(backend="memory"))
    assert isinstance(broker, MemoryBroker)
    from skein_events import Envelope, new_event_id

    broker.publish("documents.events", Envelope(id=new_event_id(), source="documents"))
    assert broker.pending("ml.documents-events") == 1
