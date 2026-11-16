"""Redis adapters: the tool-result Cache and the per-user run RateLimiter.

The rate limiter runs GCRA in a Lua script so the read-decide-write is
atomic across agent replicas. The script is the same arithmetic as
``domain/ratelimit.gcra`` (tests run both on the same inputs); time comes
from the caller, not Redis ``TIME``, so a test clock drives both.
"""

from __future__ import annotations

from typing import Any

from ..domain.ratelimit import Decision, RateLimit

# KEYS[1] = key; ARGV = now, emission interval, burst, ttl (all seconds).
# Returns {allowed (0/1), retry_after, new_tat} as strings to keep floats.
_GCRA = """
local now = tonumber(ARGV[1])
local interval = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])
local tat = tonumber(redis.call('GET', KEYS[1]) or now)
if tat < now then tat = now end
local allow_at = tat - burst * interval
if now < allow_at then
  return {'0', tostring(allow_at - now), tostring(tat)}
end
local new_tat = tat + interval
redis.call('SET', KEYS[1], tostring(new_tat), 'EX', math.ceil(ttl))
return {'1', '0', tostring(new_tat)}
"""


class RedisCache:
    """Cache over redis.asyncio."""

    def __init__(self, client: Any, prefix: str = "skein:") -> None:
        self._r = client
        self._prefix = prefix

    async def get(self, key: str) -> str | None:
        """See Cache.get."""
        value = await self._r.get(self._prefix + key)
        return value.decode() if isinstance(value, bytes) else value

    async def set(self, key: str, value: str, ttl_s: float) -> None:
        """See Cache.set."""
        await self._r.set(self._prefix + key, value, px=max(1, int(ttl_s * 1000)))

    async def incr(self, key: str) -> int:
        """See Cache.incr."""
        return int(await self._r.incr(self._prefix + key))


class RedisRateLimiter:
    """GCRA rate limiter over redis.asyncio."""

    def __init__(self, client: Any, limit: RateLimit, clock: Any, prefix: str = "skein:rl:") -> None:
        self._r = client
        self._limit = limit
        self._clock = clock
        self._prefix = prefix
        self._script = client.register_script(_GCRA)

    async def check(self, key: str) -> Decision:
        """See RateLimiter.check."""
        lim = self._limit
        ttl = lim.emission_interval_s * (lim.burst + 1) + 1
        allowed, retry, tat = await self._script(
            keys=[self._prefix + key],
            args=[repr(self._clock.now()), repr(lim.emission_interval_s), lim.burst, repr(ttl)],
        )
        as_str = [x.decode() if isinstance(x, bytes) else str(x) for x in (allowed, retry, tat)]
        return Decision(as_str[0] == "1", float(as_str[1]), float(as_str[2]))
