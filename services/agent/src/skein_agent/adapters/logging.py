"""Structured JSON logs with trace, request, user and run IDs.

Correlation IDs come from context variables set at the gRPC edge (request
and user) and when a run starts (run), plus the current OpenTelemetry span
for trace and span IDs. Because asyncio tasks copy context when created, a
run's driver task keeps the request's IDs.

Logs never contain note text, prompts, model output or tokens. That is
enforced, not just intended: only fields on ``ALLOWED_FIELDS`` are copied
from ``extra``; anything else (``markdown``, ``text``, ``token``...) is
dropped, and the message itself is a fixed string at every call site.
"""

from __future__ import annotations

import json
import logging
import sys
import time
from collections.abc import Iterator
from contextlib import contextmanager

from opentelemetry import trace

from ..observability import request_id_var, run_id_var, user_id_var

ALLOWED_FIELDS = frozenset(
    {
        "run_id",
        "chat_id",
        "tool",
        "outcome",
        "steps",
        "input_tokens",
        "output_tokens",
        "duration_ms",
        "latency_ms",
        "error_code",
        "error_type",
        "cached",
        "rpc",
        "status",
        "model",
        "port",
        "kind",
    }
)

_STANDARD = frozenset(logging.LogRecord("", 0, "", 0, "", (), None).__dict__) | {"message", "asctime"}


class JsonFormatter(logging.Formatter):
    """One JSON object per line."""

    def format(self, record: logging.LogRecord) -> str:
        """Render one record as a single JSON line."""
        out: dict[str, object] = {
            "ts": time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(record.created)) + f".{int(record.msecs):03d}Z",
            "level": record.levelname.lower(),
            "logger": record.name,
            "msg": record.getMessage(),
        }
        span = trace.get_current_span().get_span_context()
        if span.is_valid:
            out["trace_id"] = f"{span.trace_id:032x}"
            out["span_id"] = f"{span.span_id:016x}"
        for key, var in (("request_id", request_id_var), ("user_id", user_id_var), ("run_id", run_id_var)):
            if value := var.get():
                out[key] = value
        for key, value in record.__dict__.items():
            if key in ALLOWED_FIELDS and key not in _STANDARD:
                out[key] = value
        if record.exc_info and record.exc_info[0] is not None:
            # The type only: exception messages can quote request content.
            out["exception_type"] = record.exc_info[0].__name__
        return json.dumps(out, default=str, sort_keys=True)


def configure_logging(level: str = "INFO", stream: object = None) -> None:
    """Send every log record through the JSON formatter."""
    handler = logging.StreamHandler(stream or sys.stdout)  # type: ignore[arg-type]
    handler.setFormatter(JsonFormatter())
    root = logging.getLogger()
    root.handlers[:] = [handler]
    root.setLevel(level.upper())
    for noisy in ("httpx", "httpcore", "anthropic"):
        logging.getLogger(noisy).setLevel(logging.WARNING)


@contextmanager
def log_context(*, request_id: str = "", user_id: str = "", run_id: str = "") -> Iterator[None]:
    """Set correlation IDs for the duration of a block."""
    tokens = []
    for var, value in ((request_id_var, request_id), (user_id_var, user_id), (run_id_var, run_id)):
        if value:
            tokens.append((var, var.set(value)))
    try:
        yield
    finally:
        for var, tok in reversed(tokens):
            var.reset(tok)
