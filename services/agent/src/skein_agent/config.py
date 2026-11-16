"""Configuration from environment variables. Read once in ``main``.

| Variable | Default | Meaning |
|---|---|---|
| ``SKEIN_AGENT_ADDR`` | ``0.0.0.0:9090`` | gRPC listen address |
| ``SKEIN_AGENT_ADMIN_ADDR`` | ``0.0.0.0:8080`` | HTTP ``/healthz`` and ``/readyz``; empty turns it off |
| ``ANTHROPIC_API_KEY`` | (none) | required unless ``SKEIN_AGENT_MODEL_BACKEND=scripted`` |
| ``SKEIN_AGENT_MODEL`` | ``claude-sonnet-5-5`` | Anthropic model ID |
| ``SKEIN_AGENT_MODEL_BACKEND`` | ``anthropic`` | ``anthropic`` or ``scripted`` (a model that only says it is offline; for smoke tests) |
| ``SKEIN_AGENT_MAX_OUTPUT_TOKENS`` | ``4096`` | per model call |
| ``SKEIN_AGENT_PRICE_INPUT_PER_MTOK`` / ``..._OUTPUT_...`` | from the model table | USD per million tokens, for cost budgets |
| ``SKEIN_AGENT_MAX_STEPS`` / ``_MAX_TOKENS`` / ``_MAX_WALL_SECONDS`` / ``_MAX_COST_USD`` | 12 / 200000 / 120 / 0.50 | run budgets |
| ``SKEIN_AGENT_CONFIRMATION_TIMEOUT_SECONDS`` | ``600`` | how long a paused run waits |
| ``SKEIN_AGENT_RUNS_PER_MINUTE`` / ``_RUN_BURST`` | ``6`` / ``3`` | per-user run rate limit |
| ``SKEIN_AGENT_CACHE_TTL_SECONDS`` | ``30`` | tool-result cache TTL |
| ``SKEIN_AGENT_REDIS_URL`` | (none) | Redis for cache and limits; in memory if unset |
| ``SKEIN_AGENT_STORAGE`` | ``postgres`` | ``postgres`` or ``memory``, for chats, runs and graph checkpoints together; paused runs survive restarts only with ``postgres`` |
| ``SKEIN_AGENT_POSTGRES_DSN`` | (none) | required for ``postgres`` storage (schema ``agent``, migrated at startup) |
| ``SKEIN_AGENT_MIGRATIONS_DIR`` | the source tree's ``migrations/`` | where the SQL migrations are |
| ``SKEIN_AGENT_SWEEP_SECONDS`` | ``30`` | how often expired pauses and abandoned runs are ended |
| ``SKEIN_{IDENTITY,DOCUMENTS,SEARCH,ML,NOTIFICATIONS}_ADDR`` | ``localhost:5005x`` | downstream services (graph is served by search) |
| ``SKEIN_LOG_LEVEL`` | ``INFO`` | |
| ``OTEL_*`` | | see ``adapters/telemetry.py`` |

There is intentionally no variable to skip confirmations (ADR-0009).
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from dataclasses import dataclass, field

from .domain.budget import KNOWN_PRICES, Budget, Pricing
from .domain.ratelimit import RateLimit


class ConfigError(ValueError):
    """A configuration value is missing or malformed."""


@dataclass(frozen=True)
class Settings:
    """Everything ``main`` needs."""

    addr: str = "0.0.0.0:9090"
    admin_addr: str = "0.0.0.0:8080"
    model_backend: str = "anthropic"
    anthropic_api_key: str = ""
    model: str = "claude-sonnet-5-5"
    max_output_tokens: int = 4096
    pricing: Pricing = field(default_factory=lambda: KNOWN_PRICES["claude-sonnet-5-5"])
    budget: Budget = field(default_factory=Budget)
    confirmation_timeout_s: float = 600.0
    rate_limit: RateLimit = field(default_factory=lambda: RateLimit(6, 60.0, 3))
    cache_ttl_s: float = 30.0
    redis_url: str = ""
    storage: str = "postgres"
    postgres_dsn: str = ""
    sweep_interval_s: float = 30.0
    service_addrs: dict[str, str] = field(default_factory=dict)
    log_level: str = "INFO"

    def __repr__(self) -> str:  # never print the API key
        return f"Settings(addr={self.addr!r}, model={self.model!r}, backend={self.model_backend!r})"


def _num(env: Mapping[str, str], key: str, default: float, cast: type = float) -> float:
    raw = env.get(key)
    if raw is None or raw == "":
        return default
    try:
        return cast(raw)
    except ValueError as err:
        raise ConfigError(f"{key}={raw!r} is not a number") from err


def load_settings(env: Mapping[str, str] | None = None) -> Settings:
    """Build Settings from the environment, validating as it goes."""
    env = os.environ if env is None else env
    backend = env.get("SKEIN_AGENT_MODEL_BACKEND", "anthropic")
    if backend not in ("anthropic", "scripted"):
        raise ConfigError("SKEIN_AGENT_MODEL_BACKEND must be anthropic or scripted")
    api_key = env.get("ANTHROPIC_API_KEY", "")
    if backend == "anthropic" and not api_key:
        raise ConfigError("ANTHROPIC_API_KEY is required (or set SKEIN_AGENT_MODEL_BACKEND=scripted)")
    model = env.get("SKEIN_AGENT_MODEL", "claude-sonnet-5-5")
    known = KNOWN_PRICES.get(model)
    price_in = env.get("SKEIN_AGENT_PRICE_INPUT_PER_MTOK")
    price_out = env.get("SKEIN_AGENT_PRICE_OUTPUT_PER_MTOK")
    if known is None and (price_in is None or price_out is None):
        raise ConfigError(f"no price known for {model}; set SKEIN_AGENT_PRICE_INPUT_PER_MTOK and _OUTPUT_PER_MTOK")
    pricing = Pricing(
        _num(env, "SKEIN_AGENT_PRICE_INPUT_PER_MTOK", known.input_per_mtok if known else 0.0),
        _num(env, "SKEIN_AGENT_PRICE_OUTPUT_PER_MTOK", known.output_per_mtok if known else 0.0),
    )
    try:
        budget = Budget(
            max_steps=int(_num(env, "SKEIN_AGENT_MAX_STEPS", 12, int)),
            max_tokens=int(_num(env, "SKEIN_AGENT_MAX_TOKENS", 200_000, int)),
            max_wall_seconds=_num(env, "SKEIN_AGENT_MAX_WALL_SECONDS", 120.0),
            max_cost_usd=_num(env, "SKEIN_AGENT_MAX_COST_USD", 0.50),
        )
        rate = RateLimit(
            int(_num(env, "SKEIN_AGENT_RUNS_PER_MINUTE", 6, int)), 60.0, int(_num(env, "SKEIN_AGENT_RUN_BURST", 3, int))
        )
    except ValueError as err:
        raise ConfigError(str(err)) from err
    storage = env.get("SKEIN_AGENT_STORAGE", "postgres")
    if storage not in ("memory", "postgres"):
        raise ConfigError("SKEIN_AGENT_STORAGE must be postgres or memory")
    postgres_dsn = env.get("SKEIN_AGENT_POSTGRES_DSN", "")
    if storage == "postgres" and not postgres_dsn:
        raise ConfigError("SKEIN_AGENT_POSTGRES_DSN is required (or set SKEIN_AGENT_STORAGE=memory)")
    defaults = {
        "identity": "localhost:50051",
        "documents": "localhost:50052",
        "search": "localhost:50053",
        "ml": "localhost:50055",
        "notifications": "localhost:50056",
    }
    addrs = {name: env.get(f"SKEIN_{name.upper()}_ADDR", default) for name, default in defaults.items()}
    return Settings(
        addr=env.get("SKEIN_AGENT_ADDR", "0.0.0.0:9090"),
        admin_addr=env.get("SKEIN_AGENT_ADMIN_ADDR", "0.0.0.0:8080").strip(),
        model_backend=backend,
        anthropic_api_key=api_key,
        model=model,
        max_output_tokens=int(_num(env, "SKEIN_AGENT_MAX_OUTPUT_TOKENS", 4096, int)),
        pricing=pricing,
        budget=budget,
        confirmation_timeout_s=_num(env, "SKEIN_AGENT_CONFIRMATION_TIMEOUT_SECONDS", 600.0),
        rate_limit=rate,
        cache_ttl_s=_num(env, "SKEIN_AGENT_CACHE_TTL_SECONDS", 30.0),
        redis_url=env.get("SKEIN_AGENT_REDIS_URL", ""),
        storage=storage,
        postgres_dsn=postgres_dsn,
        sweep_interval_s=_num(env, "SKEIN_AGENT_SWEEP_SECONDS", 30.0),
        service_addrs=addrs,
        log_level=env.get("SKEIN_LOG_LEVEL", "INFO"),
    )
