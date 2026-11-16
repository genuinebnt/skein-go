"""Typed errors shared across the agent's layers.

Errors carry a kind tag (an enum) rather than relying on class hierarchies,
so adapters can map them to gRPC status codes with one table, and the shape
matches a Rust ``enum Error`` if this service is ever ported.
"""

from __future__ import annotations

import enum


class ErrorCode(enum.Enum):
    """Why a call to another service failed, independent of transport."""

    PERMISSION_DENIED = "permission_denied"
    UNAUTHENTICATED = "unauthenticated"
    NOT_FOUND = "not_found"
    INVALID_ARGUMENT = "invalid_argument"
    FAILED_PRECONDITION = "failed_precondition"
    RESOURCE_EXHAUSTED = "resource_exhausted"
    UNAVAILABLE = "unavailable"
    DEADLINE_EXCEEDED = "deadline_exceeded"
    UNIMPLEMENTED = "unimplemented"
    INTERNAL = "internal"


class ServiceError(Exception):
    """A downstream service refused or failed a request.

    The message is meant for the model and the user, so it never contains
    tokens; adapters build it from the status details only.
    """

    def __init__(self, code: ErrorCode, message: str) -> None:
        super().__init__(f"{code.value}: {message}")
        self.code = code
        self.message = message


class AgentErrorKind(enum.Enum):
    """Failures of the agent's own API, mapped to gRPC codes at the edge."""

    UNAUTHENTICATED = "unauthenticated"
    PERMISSION_DENIED = "permission_denied"
    NOT_FOUND = "not_found"
    INVALID_ARGUMENT = "invalid_argument"
    FAILED_PRECONDITION = "failed_precondition"
    RATE_LIMITED = "rate_limited"


class AgentError(Exception):
    """An error returned to the caller of AgentService."""

    def __init__(self, kind: AgentErrorKind, message: str, *, retry_after_s: float = 0.0) -> None:
        super().__init__(message)
        self.kind = kind
        self.message = message
        self.retry_after_s = retry_after_s
