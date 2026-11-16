"""Sends one minute of synthetic Skein telemetry to a running collector.

Purpose: prove the local pipeline end to end before any service exists, and
keep proving it as dashboards change. It emits every metric listed in
docs/observability.md with realistic attributes, a trace and log records
that carry credential-named attributes, so check.py can assert that the
dashboards' queries return data and that redaction removed the credentials.

Design: one OpenTelemetry SDK provider per fake service, so each gets its own
service.name resource exactly like a real deployment; the collector, not this
script, decides where data goes. Values are random but seeded (SKEIN_SEED) so
a failing run can be replayed. Nothing here is imported by services.

Usage: python emit.py [--seconds 60]   (OTEL_EXPORTER_OTLP_ENDPOINT, default
http://localhost:4318)
"""

from __future__ import annotations

import argparse
import logging
import os
import random
import time

from opentelemetry._logs import SeverityNumber
from opentelemetry.exporter.otlp.proto.http._log_exporter import OTLPLogExporter
from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.metrics import CallbackOptions, Histogram, Meter, Observation
from opentelemetry.sdk._logs import LoggerProvider
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

from redaction_keys import KEPT_KEYS, REDACTED_KEYS, REDACTION_SERVICE

ENDPOINT = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318").rstrip("/")
EXPORT_INTERVAL_MS = 5_000

# Bucket boundaries from docs/observability.md ("Histogram buckets"). SDK
# defaults are meant for milliseconds and would put every second-unit sample
# in the first bucket, making every quantile panel meaningless.
REQUEST_SECONDS = [0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10]
LAG_SECONDS = [0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300]
LONG_SECONDS = [0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300, 600]
STEP_COUNTS = [1, 2, 3, 5, 8, 13, 21, 34, 55]


def resource(service: str) -> Resource:
    return Resource.create(
        {
            "service.name": service,
            "service.version": "0.0.0-smoke",
            "service.instance.id": f"{service}-smoke",
            # Deliberately the deprecated key: the collector must rename it.
            "deployment.environment": "local",
        }
    )


def seconds_histogram(meter: Meter, name: str, buckets: list[float]) -> Histogram:
    return meter.create_histogram(name, unit="s", explicit_bucket_boundaries_advisory=buckets)


def meter_provider(service: str) -> MeterProvider:
    reader = PeriodicExportingMetricReader(
        OTLPMetricExporter(endpoint=f"{ENDPOINT}/v1/metrics"),
        export_interval_millis=EXPORT_INTERVAL_MS,
    )
    return MeterProvider(resource=resource(service), metric_readers=[reader])


GRPC_METHODS = {
    "identity": ["skein.v1.IdentityService/Authorize", "skein.v1.IdentityService/SignIn"],
    "documents": ["skein.v1.DocumentService/GetNote", "skein.v1.DocumentService/ApplyOps"],
    "search": ["skein.v1.SearchService/Search", "skein.v1.GraphService/BlastRadius"],
    "agent": ["skein.v1.AgentService/SendMessage", "skein.v1.AgentService/Confirm"],
}
STATUS_CODES = ["OK"] * 30 + ["NOT_FOUND", "PERMISSION_DENIED", "INTERNAL", "UNAVAILABLE"]


class Fleet:
    """All fake services and their instruments."""

    def __init__(self, rng: random.Random):
        self.rng = rng
        self.providers: list[MeterProvider] = []
        meters = {}
        for service in [*GRPC_METHODS, "gateway", REDACTION_SERVICE]:
            provider = meter_provider(service)
            self.providers.append(provider)
            meters[service] = provider.get_meter("skein.smoke")
        self.rpc_server = {
            s: meters[s].create_histogram(
                "rpc.server.call.duration", unit="s", explicit_bucket_boundaries_advisory=REQUEST_SECONDS
            )
            for s in GRPC_METHODS
        }
        gw = meters["gateway"]
        self.http_server = seconds_histogram(gw, "http.server.request.duration", REQUEST_SECONDS)
        self.http_active = gw.create_up_down_counter("http.server.active_requests", unit="{request}")
        self.rpc_client = seconds_histogram(gw, "rpc.client.call.duration", REQUEST_SECONDS)
        self.ratelimit = gw.create_counter("skein.ratelimit.decisions", unit="{request}")
        self.cache_lookups = gw.create_counter("skein.cache.lookups", unit="{lookup}")
        self.cache_invalidations = gw.create_counter("skein.cache.invalidations", unit="{entry}")

        documents = meters["documents"]
        self.publish = seconds_histogram(documents, "skein.outbox.relay.publish.duration", REQUEST_SECONDS)
        self.pending = {"documents.events": 0}
        documents.create_observable_up_down_counter(
            "skein.outbox.pending", callbacks=[self._observe_pending], unit="{row}"
        )
        documents.create_observable_gauge("skein.outbox.lag", callbacks=[self._observe_lag], unit="s")

        search = meters["search"]
        self.process = seconds_histogram(search, "skein.consumer.process.duration", REQUEST_SECONDS)
        self.event_age = seconds_histogram(search, "skein.consumer.event.age", LAG_SECONDS)

        agent = meters["agent"]
        self.run_duration = seconds_histogram(agent, "skein.agent.run.duration", LONG_SECONDS)
        self.run_steps = agent.create_histogram(
            "skein.agent.run.steps", unit="{step}", explicit_bucket_boundaries_advisory=STEP_COUNTS
        )
        self.runs_active = agent.create_up_down_counter("skein.agent.runs.active", unit="{run}")
        self.step_duration = seconds_histogram(agent, "skein.agent.step.duration", REQUEST_SECONDS)
        self.tokens = agent.create_counter("skein.agent.tokens", unit="{token}")
        self.budget = agent.create_counter("skein.agent.budget.exhaustions", unit="{run}")
        self.confirm_requests = agent.create_counter("skein.agent.confirmation.requests", unit="{confirmation}")
        self.confirm_wait = seconds_histogram(agent, "skein.agent.confirmation.wait.duration", LONG_SECONDS)

        # Metric with credential-named attributes, to check datapoint redaction.
        self.redaction_counter = meters[REDACTION_SERVICE].create_counter("skein.smoke.redaction")

    def _observe_pending(self, _: CallbackOptions):
        return [Observation(n, {"messaging.destination.name": t}) for t, n in self.pending.items()]

    def _observe_lag(self, _: CallbackOptions):
        return [Observation(self.rng.uniform(0.05, 2.0), {"messaging.destination.name": "documents.events"})]

    def tick(self) -> None:
        """One second of traffic."""
        r = self.rng
        for service, methods in GRPC_METHODS.items():
            for _ in range(20):
                status = r.choice(STATUS_CODES)
                attrs = {"rpc.system.name": "grpc", "rpc.method": r.choice(methods), "rpc.status_code": status}
                if status != "OK":
                    attrs["error.type"] = status
                self.rpc_server[service].record(r.lognormvariate(-4, 0.8), attrs)
        for _ in range(30):
            code = r.choice([200] * 25 + [404, 429, 500])
            attrs = {
                "http.request.method": r.choice(["GET", "POST"]),
                "url.scheme": "http",
                "http.route": r.choice(["/v1/notes/{note_id}", "/v1/search", "/v1/agent/chats"]),
                "http.response.status_code": code,
            }
            if code >= 500:
                attrs["error.type"] = str(code)
            self.http_server.record(r.lognormvariate(-3.5, 0.7), attrs)
            self.rpc_client.record(
                r.lognormvariate(-4, 0.6),
                {
                    "rpc.system.name": "grpc",
                    "rpc.method": "skein.v1.IdentityService/Authorize",
                    "rpc.status_code": "OK",
                    "server.address": "identity",
                },
            )
            limited = code == 429
            self.ratelimit.add(
                1,
                {
                    "skein.ratelimit.layer": "gateway",
                    "skein.ratelimit.budget": r.choice(["sync", "search", "agent", "api"]),
                    "skein.ratelimit.subject": r.choice(["user", "ip"]),
                    "skein.ratelimit.decision": "limited" if limited else "allowed",
                },
            )
            self.cache_lookups.add(
                1, {"skein.cache.name": "authorize", "skein.cache.result": "hit" if r.random() < 0.9 else "miss"}
            )
        self.http_active.add(r.choice([-1, 0, 1]))
        self.cache_invalidations.add(
            r.randint(0, 2), {"skein.cache.name": "authorize", "skein.cache.invalidation.reason": "event"}
        )

        topic = {"messaging.system": "gcp_pubsub", "messaging.destination.name": "documents.events"}
        self.pending["documents.events"] = r.randint(0, 40)
        for _ in range(10):
            ok = r.random() < 0.97
            self.publish.record(r.uniform(0.002, 0.05), topic if ok else {**topic, "error.type": "UNAVAILABLE"})
        sub = {"messaging.system": "gcp_pubsub", "messaging.destination.subscription.name": "search.documents-events"}
        for _ in range(10):
            outcome = r.choices(["applied", "duplicate", "retry", "dead_lettered"], [90, 6, 3, 1])[0]
            attrs = {**sub, "skein.event.type": "blocks_changed", "skein.consumer.outcome": outcome}
            if outcome in ("retry", "dead_lettered"):
                attrs["error.type"] = "projection_conflict"
            self.process.record(r.uniform(0.001, 0.08), attrs)
            self.event_age.record(r.uniform(0.05, 1.5), sub)

        outcome = r.choices(["completed", "cancelled", "budget_exceeded", "failed"], [80, 8, 7, 5])[0]
        run = {"skein.agent.run.outcome": outcome}
        self.run_duration.record(r.uniform(2, 40), run)
        self.run_steps.record(r.randint(1, 12), run)
        self.runs_active.add(r.choice([-1, 0, 1]))
        tool = r.choice(["read_note", "search_notes", "edit_note", "delete_note"])
        self.step_duration.record(r.uniform(0.5, 6), {"skein.agent.step.kind": "model"})
        tool_attrs = {"skein.agent.step.kind": "tool", "skein.agent.tool.name": tool}
        if r.random() < 0.05:
            tool_attrs["error.type"] = "PERMISSION_DENIED"
        self.step_duration.record(r.uniform(0.01, 0.4), tool_attrs)
        model = {"skein.agent.model": "claude-smoke"}
        self.tokens.add(r.randint(500, 4000), {**model, "skein.agent.token.type": "input"})
        self.tokens.add(r.randint(50, 800), {**model, "skein.agent.token.type": "output"})
        if outcome == "budget_exceeded":
            self.budget.add(1, {"skein.agent.budget.kind": r.choice(["steps", "tokens", "wall_time"])})
        if tool in ("edit_note", "delete_note"):
            self.confirm_requests.add(1, {"skein.agent.tool.name": tool})
            self.confirm_wait.record(
                r.uniform(1, 30),
                {
                    "skein.agent.tool.name": tool,
                    "skein.agent.confirmation.decision": r.choice(["approved", "approved", "rejected", "expired"]),
                },
            )

        self.redaction_counter.add(
            1, {key: "must-not-arrive" for key in REDACTED_KEYS} | {key: "kept" for key in KEPT_KEYS}
        )

    def shutdown(self) -> None:
        for provider in self.providers:
            provider.shutdown()


def emit_trace_and_logs(rng: random.Random) -> None:
    """One trace and two log records with credential-named attributes."""
    res = resource(REDACTION_SERVICE)
    tracer_provider = TracerProvider(resource=res)
    tracer_provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter(endpoint=f"{ENDPOINT}/v1/traces")))
    logger_provider = LoggerProvider(resource=res)
    logger_provider.add_log_record_processor(BatchLogRecordProcessor(OTLPLogExporter(endpoint=f"{ENDPOINT}/v1/logs")))
    logger = logger_provider.get_logger("skein.smoke")

    secrets = {key: "must-not-arrive" for key in REDACTED_KEYS}
    kept = {key: "kept" for key in KEPT_KEYS}
    tracer = tracer_provider.get_tracer("skein.smoke")
    with tracer.start_as_current_span("smoke.redaction", attributes=secrets | kept) as span:
        span.add_event("smoke.event", attributes=secrets | kept)
        ctx = span.get_span_context()
        for level, number in (("INFO", SeverityNumber.INFO), ("ERROR", SeverityNumber.ERROR)):
            # Inside the span, so the record takes trace_id and span_id from the
            # current context, as a real logging bridge does.
            logger.emit(
                timestamp=time.time_ns(),
                severity_text=level,
                severity_number=number,
                body=f"smoke log at {level}",
                attributes=secrets | kept | {"request_id": f"req-{rng.randrange(1 << 32):08x}", "user.id": "u-smoke"},
            )
    tracer_provider.shutdown()
    logger_provider.shutdown()
    print(f"trace {ctx.trace_id:032x} sent for {REDACTION_SERVICE}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--seconds", type=int, default=60)
    args = parser.parse_args()
    seed = int(os.environ.get("SKEIN_SEED", time.time_ns() % (1 << 31)))
    print(f"SKEIN_SEED={seed} endpoint={ENDPOINT}")
    rng = random.Random(seed)
    logging.basicConfig(level=logging.WARNING)

    emit_trace_and_logs(rng)
    fleet = Fleet(rng)
    for _ in range(args.seconds):
        fleet.tick()
        time.sleep(1)
    fleet.shutdown()
    print("metrics flushed")


if __name__ == "__main__":
    main()
