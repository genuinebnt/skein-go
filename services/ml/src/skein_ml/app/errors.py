"""Typed errors raised by the use cases; adapters map them to transport codes.

Sentinel-style classes (ADR-0012) so the gRPC adapter can map them with a
table rather than by inspecting messages.
"""

from __future__ import annotations

__all__ = ["DeadlineExceeded", "InvalidArgument", "MLError", "NotFound", "Unavailable"]


class MLError(Exception):
    """Base of every error the ml use cases raise on purpose."""


class NotFound(MLError):
    """The named note or workspace has nothing indexed (gRPC NOT_FOUND)."""


class InvalidArgument(MLError):
    """The request cannot be served as asked (gRPC INVALID_ARGUMENT)."""


class Unavailable(MLError):
    """A dependency failed in a way worth retrying (gRPC UNAVAILABLE)."""


class DeadlineExceeded(MLError):
    """The caller's deadline passed before the work finished (gRPC DEADLINE_EXCEEDED)."""
