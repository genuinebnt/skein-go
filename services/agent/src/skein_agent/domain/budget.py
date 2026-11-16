"""Per-run budgets: steps, tokens, wall time and cost.

A *step* is one model invocation. Budgets are checked **before** spending:
the tracker refuses to start a step once any limit is reached, so a run can
overshoot a token or cost limit only by the one call that crossed it, never
by starting another. Wall time is enforced by the runner with a deadline
(waiting for the user to confirm does not count), and the same deadline
bounds every tool call, so the remaining time propagates downstream.

Invariants (``check_invariants``): usage counters never go negative, steps
never exceed the step limit, and no more model calls are recorded than
steps were begun. Nothing clears ``exceeded`` once set.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass


class BudgetKind(enum.Enum):
    """Which limit a run hit."""

    STEPS = "steps"
    TOKENS = "tokens"
    COST = "cost"
    WALL_TIME = "wall_time"


class BudgetExceededError(Exception):
    """Raised when a run may not take another step."""

    def __init__(self, kind: BudgetKind) -> None:
        super().__init__(f"run budget exceeded: {kind.value}")
        self.kind = kind


@dataclass(frozen=True, slots=True)
class Budget:
    """Limits for one run. Zero means "no limit" only for cost (useful with a
    model whose price is unknown); every other limit must be positive."""

    max_steps: int = 12
    max_tokens: int = 200_000
    max_wall_seconds: float = 120.0
    max_cost_usd: float = 0.50

    def __post_init__(self) -> None:
        if self.max_steps <= 0 or self.max_tokens <= 0 or self.max_wall_seconds <= 0:
            raise ValueError("step, token and wall-time budgets must be positive")
        if self.max_cost_usd < 0:
            raise ValueError("cost budget must not be negative")


@dataclass(frozen=True, slots=True)
class Pricing:
    """Price of a model in US dollars per million tokens."""

    input_per_mtok: float
    output_per_mtok: float

    def cost(self, input_tokens: int, output_tokens: int) -> float:
        """Cost of one call."""
        return (input_tokens * self.input_per_mtok + output_tokens * self.output_per_mtok) / 1e6


# List prices from https://platform.claude.com/docs/en/about-claude/models/overview
# (checked 2026-11-16). Haiku 5.5 is listed as "from" these prices. Override
# with SKEIN_AGENT_PRICE_INPUT_PER_MTOK and SKEIN_AGENT_PRICE_OUTPUT_PER_MTOK.
KNOWN_PRICES: dict[str, Pricing] = {
    "claude-fable-5-2": Pricing(8.0, 40.0),
    "claude-opus-5-5": Pricing(4.0, 20.0),
    "claude-sonnet-5-5": Pricing(2.0, 10.0),
    "claude-haiku-5-5": Pricing(0.10, 0.50),
}


class BudgetTracker:
    """Counts what a run has spent and says whether it may continue."""

    def __init__(self, budget: Budget, pricing: Pricing) -> None:
        self.budget = budget
        self.pricing = pricing
        self.steps = 0
        self.input_tokens = 0
        self.output_tokens = 0
        self.cost_usd = 0.0
        self.exceeded: BudgetKind | None = None

    @property
    def total_tokens(self) -> int:
        """Input plus output tokens so far."""
        return self.input_tokens + self.output_tokens

    def begin_step(self) -> int:
        """Reserve the next step, or raise BudgetExceededError. Returns the
        1-based step number."""
        limit = self._limit_reached()
        if limit is not None:
            self.exceeded = limit
            raise BudgetExceededError(limit)
        self.steps += 1
        return self.steps

    def record_model_call(self, input_tokens: int, output_tokens: int) -> float:
        """Add one call's usage and return its cost."""
        if input_tokens < 0 or output_tokens < 0:
            raise ValueError("token counts must not be negative")
        cost = self.pricing.cost(input_tokens, output_tokens)
        self.input_tokens += input_tokens
        self.output_tokens += output_tokens
        self.cost_usd += cost
        return cost

    def mark_wall_time_exceeded(self) -> None:
        """Record that the runner's deadline fired."""
        self.exceeded = BudgetKind.WALL_TIME

    def _limit_reached(self) -> BudgetKind | None:
        if self.exceeded is not None:
            return self.exceeded
        if self.steps >= self.budget.max_steps:
            return BudgetKind.STEPS
        if self.total_tokens >= self.budget.max_tokens:
            return BudgetKind.TOKENS
        if self.budget.max_cost_usd > 0 and self.cost_usd >= self.budget.max_cost_usd:
            return BudgetKind.COST
        return None

    def check_invariants(self, model_calls: int | None = None) -> None:
        """Raise AssertionError if the tracker is in an impossible state.

        ``model_calls``, when given, is the number of model calls recorded
        elsewhere (the trace) and must not exceed the steps begun.
        """
        if min(self.steps, self.input_tokens, self.output_tokens) < 0 or self.cost_usd < 0:
            raise AssertionError("budget usage went negative")
        if self.steps > self.budget.max_steps:
            raise AssertionError(f"{self.steps} steps begun with a limit of {self.budget.max_steps}")
        if model_calls is not None and model_calls > self.steps:
            raise AssertionError(f"{model_calls} model calls recorded but {self.steps} steps begun")
