"""Redis implementation of the `Cache` port (optional ``redis`` extra).

Layout, under a prefix (``skein:ml:`` by default):

- ``<prefix>v:<key>``: the value, with a millisecond TTL (``SET ... PX``);
- ``<prefix>t:<tag>``: a set of keys attached to the tag, whose TTL is
  pushed out to the longest TTL of its members.

Invalidation renames the tag set to a unique temporary name first
(``RENAME`` is atomic), then reads and deletes its members. A concurrent
``set`` therefore lands in a fresh tag set rather than in one that is being
deleted, so no key escapes an invalidation it raced with. Lua would also
work, but this needs no scripting support in Redis or in test doubles.

The cache is an optimisation: connection errors are logged and behave as
misses (or no-ops), never as request failures.
"""

from __future__ import annotations

import logging
import math
import uuid
from collections.abc import Sequence

__all__ = ["RedisCache"]

log = logging.getLogger(__name__)


class RedisCache:
    """`Cache` over a ``redis.Redis`` client (``decode_responses=False``)."""

    def __init__(self, client, prefix: str = "skein:ml:"):
        import redis

        self._client = client
        self._prefix = prefix
        self._errors = (redis.RedisError, OSError)
        self._response_error = redis.ResponseError

    @classmethod
    def from_url(cls, url: str, prefix: str = "skein:ml:") -> RedisCache:
        """Connect lazily to ``url`` (``redis://host:6379/0``)."""
        try:
            import redis
        except ImportError as error:  # pragma: no cover - depends on the environment
            raise RuntimeError(
                "install the 'redis' extra: pip install 'skein-ml[redis]'"
            ) from error
        return cls(
            redis.Redis.from_url(url, socket_timeout=0.5, socket_connect_timeout=0.5), prefix
        )

    def get(self, key: str) -> bytes | None:
        """See `Cache.get`."""
        try:
            return self._client.get(self._value(key))
        except self._errors as error:
            log.warning("redis get failed", extra={"error": type(error).__name__})
            return None

    def set(self, key: str, value: bytes, ttl_seconds: float, tags: Sequence[str] = ()) -> None:
        """See `Cache.set`."""
        ttl_ms = max(1, math.ceil(ttl_seconds * 1000))
        value_key = self._value(key)
        try:
            pipe = self._client.pipeline(transaction=True)
            pipe.set(value_key, value, px=ttl_ms)
            for tag in tags:
                tag_key = self._tag(tag)
                pipe.sadd(tag_key, value_key)
                pipe.pexpire(tag_key, ttl_ms, gt=True)
                pipe.pexpire(tag_key, ttl_ms, nx=True)
            pipe.execute()
        except self._errors as error:
            log.warning("redis set failed", extra={"error": type(error).__name__})

    def invalidate_tags(self, tags: Sequence[str]) -> int:
        """See `Cache.invalidate_tags`."""
        dropped = 0
        for tag in tags:
            doomed = f"{self._prefix}d:{uuid.uuid4().hex}"
            try:
                try:
                    self._client.rename(self._tag(tag), doomed)
                except self._response_error:
                    continue  # no such tag: nothing to drop
                members = list(self._client.smembers(doomed))
                pipe = self._client.pipeline(transaction=False)
                if members:
                    pipe.delete(*members)
                pipe.delete(doomed)
                results = pipe.execute()
                dropped += int(results[0]) if members else 0
            except self._errors as error:
                log.warning("redis invalidation failed", extra={"error": type(error).__name__})
        return dropped

    def _value(self, key: str) -> str:
        return f"{self._prefix}v:{key}"

    def _tag(self, tag: str) -> str:
        return f"{self._prefix}t:{tag}"
