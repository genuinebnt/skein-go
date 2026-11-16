"""GCRA (generic cell rate algorithm) for per-user agent-run limits.

The gateway limits requests with GCRA in Redis (docs/architecture.md section
5); the agent applies the same algorithm to runs as a second line of
defence, because a run is far more expensive than a request.

GCRA keeps one number per key, the *theoretical arrival time* (TAT): when
the key would next be allowed if it had used its rate exactly. With
emission interval ``T = period / rate`` and burst ``B``:

- allow if ``now >= TAT - B*T`` (with ``TAT = max(TAT, now)``), then set
  ``TAT += T``;
- otherwise deny, with ``retry_after = TAT - B*T - now``.

This module holds the arithmetic only. ``fakes.ratelimit`` runs it over a
dict and ``adapters.redis_ratelimit`` runs the identical steps in a Lua
script, so both agree on every decision.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class RateLimit:
    """``rate`` runs per ``period_s`` seconds, with ``burst`` extra allowed at once."""

    rate: int
    period_s: float
    burst: int

    def __post_init__(self) -> None:
        if self.rate <= 0 or self.period_s <= 0 or self.burst < 0:
            raise ValueError("rate and period must be positive, burst non-negative")

    @property
    def emission_interval_s(self) -> float:
        """Seconds between runs at the sustained rate."""
        return self.period_s / self.rate


@dataclass(frozen=True, slots=True)
class Decision:
    """The outcome of one check."""

    allowed: bool
    retry_after_s: float
    new_tat: float


def gcra(limit: RateLimit, tat: float | None, now: float) -> Decision:
    """Decide one request given the stored TAT (None for a new key)."""
    interval = limit.emission_interval_s
    tat = now if tat is None else max(tat, now)
    allow_at = tat - limit.burst * interval
    if now < allow_at:
        return Decision(False, allow_at - now, tat)
    return Decision(True, 0.0, tat + interval)
