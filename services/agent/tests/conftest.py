"""Shared fixtures. Nothing here opens a network connection: every port is
an in-memory fake, the model is scripted, and OpenTelemetry has no SDK
installed (its API is a no-op)."""

from __future__ import annotations

import os
from collections.abc import Callable
from typing import Any

import pytest

os.environ.setdefault("OTEL_SDK_DISABLED", "true")

from skein_agent.evals import fixture
from skein_agent.fakes.harness import Harness, build_harness


@pytest.fixture
def fx() -> fixture.Fixture:
    """A fresh copy of the eval workspace."""
    return fixture.seed()


@pytest.fixture
def make_harness(fx: fixture.Fixture) -> Callable[..., Harness]:
    """Build a harness on the fixture workspace with a script."""

    def make(script: list[Any], **kwargs: Any) -> Harness:
        return build_harness(fx.workspace, fx.tokens, script=script, **kwargs)

    return make
