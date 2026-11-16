"""Structured JSON logs, OpenTelemetry traces and metrics.

Logs: one JSON object per line on stdout with ``time`` (RFC 3339, UTC),
``level``, ``msg``, ``service``, ``version``, ``env``, then ``trace_id`` and
``span_id`` from the active span and ``request_id`` from the request
context, then the record's extra fields. Field names match the Go
``platform/logging`` package so every service's logs query the same way.

Redaction: values of sensitive keys (passwords, tokens, secrets,
authorization, cookies) are replaced by ``[REDACTED]``. Keys that would
carry note contents (``text``, ``query``, ``title``, ``content``, ``body``)
are redacted too, as a safety net behind the rule that contents are never
logged.

Telemetry follows the standard environment variables, as the Go platform
does: ``OTEL_SDK_DISABLED=true`` turns everything off; otherwise
``OTEL_TRACES_EXPORTER`` and ``OTEL_METRICS_EXPORTER`` are ``otlp``
(default) or ``none``, and the OTLP exporters read
``OTEL_EXPORTER_OTLP_ENDPOINT`` and friends themselves. Tests never call
`setup_telemetry`, so they get the API's no-op providers.
"""

from __future__ import annotations

import contextlib
import contextvars
import datetime as dt
import json
import logging
import sys
import uuid
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field

from opentelemetry import metrics, trace

__all__ = [
    "REDACTED",
    "JsonFormatter",
    "ServiceMetrics",
    "Telemetry",
    "configure_logging",
    "current_request_id",
    "is_sensitive_key",
    "new_request_id",
    "request_context",
    "setup_telemetry",
]

REDACTED = "[REDACTED]"
_SENSITIVE_PARTS = (
    "password",
    "passwd",
    "secret",
    "token",
    "authorization",
    "cookie",
    "api_key",
    "apikey",
    "credential",
    "private_key",
)
_CONTENT_KEYS = frozenset({"text", "query", "title", "content", "body", "note_text"})
# Counting fields that merely mention tokens are not secrets.
_ALLOWED_TOKEN_KEYS = frozenset({"input_tokens", "output_tokens", "max_tokens", "tokens"})

_request_id: contextvars.ContextVar[str] = contextvars.ContextVar("request_id", default="")

_RESERVED = frozenset(vars(logging.LogRecord("", 0, "", 0, "", None, None))) | {
    "message",
    "asctime",
    "taskName",
}


def is_sensitive_key(key: str) -> bool:
    """Whether a log field's value must never be written."""
    lowered = key.lower()
    if lowered in _ALLOWED_TOKEN_KEYS:
        return False
    return lowered in _CONTENT_KEYS or any(part in lowered for part in _SENSITIVE_PARTS)


def new_request_id() -> str:
    """A fresh request id (UUIDv4 hex; the gateway normally supplies one)."""
    return uuid.uuid4().hex


def current_request_id() -> str:
    """The request id of the current context, or ''."""
    return _request_id.get()


@contextlib.contextmanager
def request_context(request_id: str) -> Iterator[None]:
    """Attach ``request_id`` to every log record written inside the block."""
    token = _request_id.set(request_id)
    try:
        yield
    finally:
        _request_id.reset(token)


class JsonFormatter(logging.Formatter):
    """Formats records as single-line JSON (see module doc for the fields)."""

    def __init__(self, service: str, version: str = "", env: str = ""):
        super().__init__()
        self._static = {"service": service, "version": version, "env": env}

    def format(self, record: logging.LogRecord) -> str:
        """Render one record."""
        out: dict[str, object] = {
            "time": dt.datetime.fromtimestamp(record.created, dt.UTC)
            .isoformat(timespec="milliseconds")
            .replace("+00:00", "Z"),
            "level": record.levelname,
            "msg": record.getMessage(),
            **self._static,
            "logger": record.name,
        }
        context = trace.get_current_span().get_span_context()
        if context.is_valid:
            out["trace_id"] = format(context.trace_id, "032x")
            out["span_id"] = format(context.span_id, "016x")
        request_id = _request_id.get()
        if request_id:
            out["request_id"] = request_id
        for key, value in record.__dict__.items():
            if key in _RESERVED or key in out:
                continue
            out[key] = REDACTED if is_sensitive_key(key) else _jsonable(value)
        if record.exc_info:
            out["error"] = self.formatException(record.exc_info)
        return json.dumps(out, default=str)


def _jsonable(value: object) -> object:
    if isinstance(value, str | int | float | bool) or value is None:
        return value
    if isinstance(value, dict):
        return {k: REDACTED if is_sensitive_key(str(k)) else _jsonable(v) for k, v in value.items()}
    if isinstance(value, list | tuple):
        return [_jsonable(v) for v in value]
    return str(value)


def configure_logging(
    service: str, version: str = "", env: str = "", level: str = "INFO", stream=None
) -> logging.Handler:
    """Install the JSON formatter on the root logger; returns the handler."""
    handler = logging.StreamHandler(stream or sys.stdout)
    handler.setFormatter(JsonFormatter(service, version, env))
    root = logging.getLogger()
    for existing in list(root.handlers):
        root.removeHandler(existing)
    root.addHandler(handler)
    root.setLevel(level.upper())
    return handler


@dataclass
class Telemetry:
    """Configured providers and how to flush them."""

    enabled: bool
    tracer: trace.Tracer
    meter: metrics.Meter
    _shutdowns: list[Callable[[], None]] = field(default_factory=list)

    def shutdown(self) -> None:
        """Flush and stop exporters; safe on a disabled setup."""
        for stop in self._shutdowns:
            with contextlib.suppress(Exception):
                stop()


def setup_telemetry(
    service: str,
    version: str = "",
    env: str = "",
    lookup: Callable[[str], str | None] | None = None,
) -> Telemetry:
    """Install OTLP trace and metric providers as globals, per ``OTEL_*``."""
    import os

    lookup = lookup or os.environ.get
    if (lookup("OTEL_SDK_DISABLED") or "").strip().lower() == "true":
        return Telemetry(False, trace.get_tracer(service), metrics.get_meter(service))

    from opentelemetry.sdk.resources import Resource

    resource = Resource.create(
        {"service.name": service, "service.version": version, "deployment.environment": env}
    )
    shutdowns: list[Callable[[], None]] = []
    if _exporter_enabled(lookup, "TRACES"):
        from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import BatchSpanProcessor

        tracer_provider = TracerProvider(resource=resource)
        tracer_provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
        trace.set_tracer_provider(tracer_provider)
        shutdowns.append(tracer_provider.shutdown)
    if _exporter_enabled(lookup, "METRICS"):
        from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import OTLPMetricExporter
        from opentelemetry.sdk.metrics import MeterProvider
        from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader

        meter_provider = MeterProvider(
            resource=resource, metric_readers=[PeriodicExportingMetricReader(OTLPMetricExporter())]
        )
        metrics.set_meter_provider(meter_provider)
        shutdowns.append(meter_provider.shutdown)
    return Telemetry(
        True, trace.get_tracer(service, version), metrics.get_meter(service, version), shutdowns
    )


def _exporter_enabled(lookup: Callable[[str], str | None], signal: str) -> bool:
    value = (lookup(f"OTEL_{signal}_EXPORTER") or "otlp").strip().lower()
    if value not in ("otlp", "none"):
        raise ValueError(f"OTEL_{signal}_EXPORTER={value!r}: only 'otlp' and 'none' are supported")
    return value == "otlp"


class ServiceMetrics:
    """The service's instruments, created once from a meter."""

    def __init__(self, meter: metrics.Meter):
        self.rpc_requests = meter.create_counter(
            "skein.ml.rpc.requests", description="gRPC requests by method and status code"
        )
        self.rpc_duration = meter.create_histogram(
            "skein.ml.rpc.duration", unit="ms", description="gRPC handler latency"
        )
        self.events = meter.create_counter(
            "skein.ml.events", description="documents.events by payload kind and outcome"
        )
        self.search_recall = meter.create_histogram(
            "skein.ml.search.recall", description="Measured recall@k of approximate searches"
        )
