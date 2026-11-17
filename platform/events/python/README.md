# skein-events

The Python half of `platform/events`: publish and consume Skein event
envelopes over Google Cloud Pub/Sub (or its emulator), Redis Streams, or an
in-process broker, chosen by `EVENTS_BACKEND` (ADR-0022,
[docs/local-first.md](../../../docs/local-first.md)). The Redis wire format
is [docs/spec/events-redis.md](../../../docs/spec/events-redis.md), shared
with the Go backend, so Go and Python services exchange events.

The ml and agent services depend on it by path, like `skein-proto`.

```
python3 -m venv .venv && .venv/bin/pip install -e '.[dev]'
.venv/bin/pytest                      # memory and fakeredis
SKEIN_TEST_REDIS_URL=redis://127.0.0.1:26379/15 \
SKEIN_TEST_PUBSUB_EMULATOR_HOST=127.0.0.1:28085 \
  .venv/bin/pytest -m integration     # real redis-server and the emulator (needs the pubsub extra)
```
