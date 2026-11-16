"""Tracing and metrics instruments, through the OpenTelemetry API only.

The API is a no-op until an SDK provider is installed, which only
``adapters/telemetry.py`` does (from ``OTEL_*`` configuration, in ``main``).
Tests never install one, so instrumentation costs nothing there and needs no
collector. Attribute values are IDs, tool names, outcomes and counts, never
note text, prompts or tokens.
"""

from __future__ import annotations

import contextvars
from dataclasses import dataclass

from opentelemetry import metrics, trace

INSTRUMENTATION_NAME = "skein_agent"

# Correlation IDs for logs. Set at the gRPC edge (request, user) and by each
# run's driver task (run); asyncio tasks copy them when created.
request_id_var: contextvars.ContextVar[str] = contextvars.ContextVar("request_id", default="")
user_id_var: contextvars.ContextVar[str] = contextvars.ContextVar("user_id", default="")
run_id_var: contextvars.ContextVar[str] = contextvars.ContextVar("run_id", default="")


def tracer() -> trace.Tracer:
    """The service's tracer (resolved lazily so a provider set later applies)."""
    return trace.get_tracer(INSTRUMENTATION_NAME)


@dataclass(frozen=True)
class Instruments:
    """Metric instruments for runs, steps, tokens, cost and latency."""

    runs: metrics.Counter
    steps: metrics.Histogram
    tokens: metrics.Counter
    cost: metrics.Counter
    run_latency: metrics.Histogram
    tool_calls: metrics.Counter
    tool_latency: metrics.Histogram
    confirmations: metrics.Counter
    rate_limited: metrics.Counter

    @classmethod
    def create(cls, meter: metrics.Meter | None = None) -> Instruments:
        """Build instruments on ``meter`` (the global one by default)."""
        m = meter or metrics.get_meter(INSTRUMENTATION_NAME)
        return cls(
            runs=m.create_counter("agent.runs", unit="{run}", description="Runs finished, by outcome"),
            steps=m.create_histogram("agent.run.steps", unit="{step}", description="Model calls per run"),
            tokens=m.create_counter("agent.tokens", unit="{token}", description="Model tokens, by direction"),
            cost=m.create_counter("agent.cost", unit="USD", description="Estimated model cost"),
            run_latency=m.create_histogram("agent.run.duration", unit="s", description="Run wall time"),
            tool_calls=m.create_counter("agent.tool.calls", unit="{call}", description="Tool calls"),
            tool_latency=m.create_histogram("agent.tool.duration", unit="ms", description="Tool latency"),
            confirmations=m.create_counter(
                "agent.confirmations", unit="{request}", description="Confirmation requests, by answer"
            ),
            rate_limited=m.create_counter("agent.rate_limited", unit="{run}", description="Runs refused"),
        )
