"""Fakes for the clock, cache and rate limiter."""

from __future__ import annotations

import time

from ..domain.ratelimit import Decision, RateLimit, gcra


class SystemClock:
    """The real clock."""

    def now(self) -> float:
        """See Clock.now."""
        return time.time()

    def monotonic(self) -> float:
        """See Clock.monotonic."""
        return time.monotonic()


class FakeClock:
    """A clock tests move by hand."""

    def __init__(self, start: float = 1_800_000_000.0) -> None:
        self._now = start
        self._mono = 0.0

    def now(self) -> float:
        """See Clock.now."""
        return self._now

    def monotonic(self) -> float:
        """See Clock.monotonic."""
        return self._mono

    def advance(self, seconds: float) -> None:
        """Move both clocks forward."""
        self._now += seconds
        self._mono += seconds


class InMemoryCache:
    """A Cache with TTLs measured on an injectable clock."""

    def __init__(self, clock: FakeClock | SystemClock | None = None) -> None:
        self._clock = clock or SystemClock()
        self._data: dict[str, tuple[str, float]] = {}
        self.hits = 0
        self.misses = 0

    async def get(self, key: str) -> str | None:
        """See Cache.get."""
        item = self._data.get(key)
        if item is None or item[1] <= self._clock.now():
            self._data.pop(key, None)
            self.misses += 1
            return None
        self.hits += 1
        return item[0]

    async def set(self, key: str, value: str, ttl_s: float) -> None:
        """See Cache.set."""
        self._data[key] = (value, self._clock.now() + ttl_s)

    async def incr(self, key: str) -> int:
        """See Cache.incr."""
        current = self._data.get(key)
        value = int(current[0]) + 1 if current else 1
        self._data[key] = (str(value), float("inf"))
        return value

    def keys(self) -> list[str]:
        """Live keys, sorted (for assertions)."""
        now = self._clock.now()
        return sorted(k for k, (_, exp) in self._data.items() if exp > now)


class InMemoryRateLimiter:
    """GCRA over a dict, using the same arithmetic as the Redis script."""

    def __init__(self, limit: RateLimit, clock: FakeClock | SystemClock | None = None) -> None:
        self._limit = limit
        self._clock = clock or SystemClock()
        self._tat: dict[str, float] = {}

    async def check(self, key: str) -> Decision:
        """See RateLimiter.check."""
        decision = gcra(self._limit, self._tat.get(key), self._clock.now())
        if decision.allowed:
            self._tat[key] = decision.new_tat
        return decision


class AllowAll:
    """A rate limiter that never refuses (tests that are not about limits)."""

    async def check(self, key: str) -> Decision:
        """See RateLimiter.check."""
        return Decision(True, 0.0, 0.0)
