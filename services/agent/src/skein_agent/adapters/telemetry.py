"""OpenTelemetry SDK setup from the standard ``OTEL_*`` environment.

Called once from ``main``. Tests never call it, so the API stays a no-op
there. Configuration follows the OpenTelemetry spec variables:

- ``OTEL_SDK_DISABLED=true`` turns everything off.
- ``OTEL_EXPORTER_OTLP_ENDPOINT`` (or the per-signal ``..._TRACES_`` and
  ``..._METRICS_`` variants) enables OTLP/gRPC export; without an endpoint
  nothing is exported and no provider is installed.
- ``OTEL_SERVICE_NAME`` (default ``skein-agent``) and
  ``OTEL_RESOURCE_ATTRIBUTES`` describe the resource.
- ``OTEL_METRIC_EXPORT_INTERVAL`` (milliseconds) sets the metric period.

The exporters read the remaining ``OTEL_EXPORTER_OTLP_*`` variables
(headers, TLS, timeouts) themselves.
"""

from __future__ import annotations

import os
from collections.abc import Callable, Mapping

from opentelemetry import metrics, trace


def telemetry_enabled(env: Mapping[str, str]) -> bool:
    """Whether to install SDK providers."""
    if env.get("OTEL_SDK_DISABLED", "").strip().lower() == "true":
        return False
    return any(
        env.get(k)
        for k in (
            "OTEL_EXPORTER_OTLP_ENDPOINT",
            "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
            "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
        )
    )


def configure_telemetry(env: Mapping[str, str] | None = None) -> Callable[[], None]:
    """Install tracer and meter providers if enabled; return a shutdown
    function that flushes them (a no-op when disabled)."""
    env = os.environ if env is None else env
    if not telemetry_enabled(env):
        return lambda: None

    from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import OTLPMetricExporter
    from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
    from opentelemetry.sdk.metrics import MeterProvider
    from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
    from opentelemetry.sdk.resources import Resource
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import BatchSpanProcessor

    resource = Resource.create({"service.name": env.get("OTEL_SERVICE_NAME", "skein-agent")})
    tracer_provider = TracerProvider(resource=resource)
    tracer_provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    trace.set_tracer_provider(tracer_provider)

    interval = int(env.get("OTEL_METRIC_EXPORT_INTERVAL", "15000"))
    reader = PeriodicExportingMetricReader(OTLPMetricExporter(), export_interval_millis=interval)
    meter_provider = MeterProvider(resource=resource, metric_readers=[reader])
    metrics.set_meter_provider(meter_provider)

    def shutdown() -> None:
        tracer_provider.shutdown()
        meter_provider.shutdown()

    return shutdown
