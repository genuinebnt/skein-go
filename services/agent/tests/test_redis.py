"""Redis adapters against fakeredis (in-process, with Lua via lupa)."""

from __future__ import annotations

import os
import random

import pytest

fakeredis = pytest.importorskip("fakeredis")

from skein_agent.adapters.redis_store import RedisCache, RedisRateLimiter  # noqa: E402
from skein_agent.domain.ratelimit import RateLimit, gcra  # noqa: E402
from skein_agent.fakes.infra import FakeClock  # noqa: E402


async def test_redis_cache_round_trips_and_increments() -> None:
    cache = RedisCache(fakeredis.FakeAsyncRedis())
    assert await cache.get("k") is None
    await cache.set("k", "v", ttl_s=30)
    assert await cache.get("k") == "v"
    assert [await cache.incr("g"), await cache.incr("g")] == [1, 2]


async def test_redis_rate_limiter_makes_the_same_decisions_as_the_domain_gcra() -> None:
    seed = int(os.environ.get("SKEIN_SEED", random.randrange(1 << 32)))
    print(f"SKEIN_SEED={seed}")
    rng = random.Random(seed)
    clock = FakeClock()
    limit = RateLimit(rate=3, period_s=60, burst=2)
    limiter = RedisRateLimiter(fakeredis.FakeAsyncRedis(), limit, clock)
    tat = None
    for _ in range(200):
        clock.advance(rng.choice([0, 0, 1, 5, 20, 40]))
        oracle = gcra(limit, tat, clock.now())
        if oracle.allowed:
            tat = oracle.new_tat
        got = await limiter.check("user-1")
        assert got.allowed == oracle.allowed, f"seed {seed}"
        assert got.retry_after_s == pytest.approx(oracle.retry_after_s, abs=1e-6)


async def test_redis_rate_limiter_keys_are_independent_per_user() -> None:
    limiter = RedisRateLimiter(fakeredis.FakeAsyncRedis(), RateLimit(1, 60, 0), FakeClock())
    assert (await limiter.check("a")).allowed
    assert not (await limiter.check("a")).allowed
    assert (await limiter.check("b")).allowed
