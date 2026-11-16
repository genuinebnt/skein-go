"""What a tool does to the world, and what that implies.

Each tool declares one ``Effect``. Confirmation and caching are *derived*
from it here, not configured per tool or per deployment: ADR-0009 requires
that destructive and outward-facing actions always ask the user, so there is
deliberately no flag, environment variable or argument that turns the gate
off.

- READ: no change; results may be cached per user.
- WRITE: a reversible change the user can see and undo (create, rename, a
  suggestion). Runs without confirmation; clears the user's cache.
- DESTRUCTIVE: removes or hides something (delete, even to the trash).
- OUTWARD: lets someone else see or do something, or contacts someone
  (change a role, create a share link, invite, send email).
"""

from __future__ import annotations

import enum


class Effect(enum.Enum):
    """The kind of side effect a tool has."""

    READ = "read"
    WRITE = "write"
    DESTRUCTIVE = "destructive"
    OUTWARD = "outward"


def requires_confirmation(effect: Effect) -> bool:
    """Whether the user must approve each call before it runs."""
    return effect in (Effect.DESTRUCTIVE, Effect.OUTWARD)


def is_cacheable(effect: Effect) -> bool:
    """Whether a result may be served from the per-user cache."""
    return effect is Effect.READ


def invalidates_cache(effect: Effect) -> bool:
    """Whether a successful call makes the user's cached reads stale."""
    return effect is not Effect.READ
