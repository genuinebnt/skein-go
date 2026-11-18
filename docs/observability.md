# Observability

How Skein services report what they are doing, what every service must emit,
and how the same signals reach Google Cloud. The local stack implements all of
it with `make up`, with no cloud account.

This document is the contract between services and dashboards. A metric or
label named here is what the dashboards in
`deploy/observability/grafana/dashboards` query. Renaming one breaks a panel
without any error message, so change this document, the dashboards and the
code together, and run `make stack-smoke`.

## Quick start

```
make up            # infrastructure plus the observability profile
make urls          # every local URL
make stack-smoke   # send synthetic telemetry, check every dashboard query and redaction
make up-min        # infrastructure only (Postgres, Redis, Pub/Sub, Mailpit)
make stack-check   # static checks of every config, no running stack needed
```

Grafana opens on the service overview, with all dashboards in the Skein folder.
Grafana has no login locally; its port binds to 127.0.0.1 only.

## Pipeline

```
 services (Go: otel-go SDK, Python: opentelemetry-sdk)
   │  OTLP gRPC :4317 / OTLP HTTP :4318     (services know only this endpoint)
   ▼
 OpenTelemetry Collector (contrib 0.165.0)  deploy/observability/otel-collector.yaml
   memory_limiter → resource → transform/normalize_resource → transform/redact → batch
   ├── traces  ── OTLP gRPC ──────────▶ Jaeger 2.20.0        :16686
   ├── metrics ── Prometheus remote write ▶ Prometheus 3.16.0 :9090
   └── logs    ── OTLP HTTP (/otlp) ──▶ Loki 3.7.10          :3100
                                           │
                       Grafana 13.3.1 :3000 ◀┘ (all three, cross-linked)
```

Services export over OTLP to the collector and nowhere else. Backends are the
collector's business, which is what lets the cloud swap them for Google's
(see [GCP mapping](#gcp-mapping)) without a code change. Locally, a service
running on the host uses `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317`;
inside compose, `http://otel-collector:4317`.

Why metrics are pushed with remote write rather than scraped through the
collector's `prometheus` exporter: the cloud path is push too, so only the
exporter differs between environments; samples keep the SDK's timestamps
instead of being re-sampled at a scrape interval; and the collector holds no
per-series state, so a cardinality mistake cannot become a collector memory
problem. The cost is one Prometheus flag, `--web.enable-remote-write-receiver`.

Grafana links the signals: a histogram exemplar opens its trace, a log line's
`trace_id` opens the trace in Jaeger, and a span opens its service's logs for
that trace in Loki.

## Signals

| Signal | Use it for | Local backend | Kept |
|---|---|---|---|
| Traces | One request's path across services and Pub/Sub, and where its time went | Jaeger (memory, 50 000 traces) | until restart |
| Metrics | Rates, error ratios, latency distributions, queue depths, budgets: what dashboards and alerts read | Prometheus | 7 days |
| Logs | What happened and why, for one request or one event, with trace correlation | Loki | 7 days |

Rule of thumb: metrics say *that* something is wrong, traces say *where*, logs
say *why*.

## Semantic conventions

RPC and HTTP metrics and spans follow OpenTelemetry semantic conventions
**v1.45.0**. That release made the RPC spans and call-duration metrics stable,
with `rpc.system.name`, a fully qualified `rpc.method` and a string
`rpc.status_code`. The HTTP conventions have been stable since v1.23.

Everything Skein-specific uses the `skein.` prefix, as the conventions ask of
custom names. Two areas that have conventions still use custom names, on
purpose:

- **Messaging** (outbox, consumers): the messaging metrics are still
  Development in v1.45.0. The metrics below are `skein.*`, but their
  attributes reuse the convention's attribute names (`messaging.system`,
  `messaging.destination.name`, `messaging.destination.subscription.name`),
  so a later move to the official metric names changes names, not labels.
- **Agent**: the GenAI conventions moved to their own repository and are
  still Development and changing (the old `gen_ai.client.token.usage` has
  already been split into per-type counters). Agent metrics are `skein.agent.*`
  until those conventions are stable.

Prometheus names are derived mechanically by the collector: dots become
underscores, the unit is appended (`s` becomes `_seconds`), monotonic counters
get `_total`, annotation units such as `{request}` are dropped, and histograms
become `_bucket`, `_sum` and `_count`. Attributes become labels the same way
(`rpc.method` becomes `rpc_method`). The tables below give both forms.

## Resource attributes

Every service sets these on its OpenTelemetry resource, for traces, metrics
and logs alike.

| Attribute | Required | Value | Prometheus |
|---|---|---|---|
| `service.name` | yes | `gateway`, `identity`, `documents`, `search`, `ml`, `agent`, `notifications` | `job` |
| `service.version` | yes | the build's version, for example the git describe output | `target_info{service_version}` |
| `deployment.environment.name` | yes | `local`, `kind`, `staging`, `prod` | `target_info` |
| `service.instance.id` | recommended | pod name in Kubernetes, a random UUID otherwise | `instance` |

`deployment.environment.name` is the current name of what older material calls
`deployment.environment`; semantic conventions renamed it in v1.27. The
collector renames the old key if an SDK still sends it, and fills in the
collector's own environment if a service sends neither, so the label is never
missing. Do not set `service.namespace`: Prometheus would turn `job` into
`skein/gateway` and every dashboard filters on the bare service name.

Set them through the standard environment variables, so no code knows its
environment:

```
OTEL_SERVICE_NAME=documents
OTEL_RESOURCE_ATTRIBUTES=service.version=1.4.0,deployment.environment.name=local
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317
```

Resource attributes are not copied onto every metric series (that would
multiply series by every SDK and host attribute). Join `target_info` when a
query needs one:
`... * on (job, instance) group_left (service_version) target_info`.

## Logs

Every service writes one JSON object per line to stdout **and** exports the
same records over OTLP to the collector. Stdout is for `kubectl logs` and
`docker compose logs`; OTLP is what reaches Loki locally and Cloud Logging in
GCP. In Go this is `log/slog` with a JSON handler plus the `otelslog` bridge;
in Python, the standard `logging` module with a JSON formatter plus the
OpenTelemetry logging handler.

### Fields every service emits

| JSON field | Type | When | Meaning | OTLP log record |
|---|---|---|---|---|
| `time` | string | always | RFC 3339 with nanoseconds, UTC: `2026-11-16T19:56:03.123456789Z` | `Timestamp` |
| `level` | string | always | `DEBUG`, `INFO`, `WARN` or `ERROR` | `SeverityText`, with the matching `SeverityNumber` |
| `msg` | string | always | Constant human-readable message; variable parts go in fields | `Body` |
| `service` | string | always | Same as `service.name` | resource `service.name` |
| `trace_id` | string | inside a trace | 32 lowercase hex characters (W3C) | `TraceId` |
| `span_id` | string | inside a span | 16 lowercase hex characters | `SpanId` |
| `request_id` | string | while handling a request | ID assigned by the gateway (below) | attribute `request_id` |
| `user_id` | string | when the caller is known | Skein user ID of the subject (for agent runs, the user the agent acts for) | attribute `user.id` |

Optional fields are allowed and encouraged when they help: `error` (the error
string), `rpc_method`, `note_id`, `workspace_id`, `event_id`, `run_id`,
`subscription`. Keys are `snake_case` in JSON and dotted in OTLP attributes.

Example:

```json
{"time":"2026-11-16T19:56:03.123456789Z","level":"ERROR","msg":"apply ops rejected","service":"documents","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","request_id":"01JD3Q4W8Y6V2N7R5K0M9T1XHC","user_id":"usr_01JD3PZ","note_id":"nte_01JD3Q1","error":"operation references unknown parent"}
```

Request IDs: the gateway assigns one per incoming HTTP request or WebSocket
message (UUIDv7), returns it as `X-Request-Id`, and forwards it to services as
gRPC metadata `x-request-id`; services copy it into their log context. Event
consumers have no request; they log `event_id` (the `Event.id` envelope field)
instead, and continue the publisher's trace from `Event.traceparent`.

Never log secrets (passwords, tokens, keys, cookies, OAuth codes), note
contents, block text, search queries or agent prompts and replies. Log IDs and
sizes instead. The collector's redaction (below) is a safety net for attribute
keys, not permission: it cannot see inside `msg`.

In Loki, `service.name` and `deployment.environment.name` are index labels
(`service_name`, `deployment_environment_name`), and `trace_id`, `span_id`,
`request_id`, `user_id` and `severity_text` are structured metadata. They can
be filtered on but do not create streams, so per-request values never inflate
the index. Loki also derives `detected_level`.

```
{service_name="documents"} | request_id="01JD3Q4W8Y6V2N7R5K0M9T1XHC"
{service_name=~".+"} | detected_level="error" | user_id="usr_01JD3PZ"
```

## Metrics

### Label rules

- Labels must have a small, fixed set of values. Never use user IDs, note
  IDs, workspace IDs, request IDs, IP addresses, raw URL paths, Redis keys or
  error messages as metric attributes; those belong in logs and spans.
  `http.route` is the route template (`/v1/notes/{note_id}`), never the path.
- `error.type` is set if and only if the operation failed: the gRPC status
  code, the HTTP status code as a string, or a short fixed error class such as
  `timeout` or `projection_conflict`. Panels count failures with
  `error_type!=""` and successes with `error_type=""`.
- Every duration is in seconds (unit `s`), with the buckets listed under
  [Histogram buckets](#histogram-buckets).

### Services: RPC and HTTP (semantic conventions v1.45.0)

Instrument with the OpenTelemetry gRPC and HTTP instrumentation (`otelgrpc`
and `otelhttp` in Go, `opentelemetry-instrumentation-grpc` in Python),
configured for the v1.45.0 conventions, rather than by hand.

| Metric | Type | Unit | Attributes | Prometheus |
|---|---|---|---|---|
| `rpc.server.call.duration` | histogram | `s` | `rpc.system.name` (`grpc`), `rpc.method` (`skein.v1.DocumentService/GetNote`), `rpc.status_code` (`OK`, `NOT_FOUND`, ...), `error.type` | `rpc_server_call_duration_seconds_*` |
| `rpc.client.call.duration` | histogram | `s` | as above, plus `server.address` (`identity`) | `rpc_client_call_duration_seconds_*` |
| `http.server.request.duration` | histogram | `s` | `http.request.method`, `http.route`, `http.response.status_code`, `url.scheme`, `error.type` | `http_server_request_duration_seconds_*` |
| `http.server.active_requests` | up-down counter | `{request}` | `http.request.method`, `url.scheme` | `http_server_active_requests` |

Only the gateway serves HTTP. Streaming RPCs (`Sync`, `SendMessage`,
`Subscribe`) record one duration per stream, which is why the dashboards'
per-method table matters: a long-lived stream's p99 says nothing about unary
latency.

**Errors for RED.** Semantic conventions treat every non-OK status as an
error by default and allow narrowing it. The service overview's error ratio
counts only server faults: `UNKNOWN`, `DEADLINE_EXCEEDED`, `UNIMPLEMENTED`,
`INTERNAL`, `UNAVAILABLE` and `DATA_LOSS` for gRPC, `5xx` for HTTP. Client
faults such as `INVALID_ARGUMENT`, `NOT_FOUND` and `PERMISSION_DENIED` have
their own panel. They matter, but they do not mean the service is broken.

### Outbox relays

Emitted by each service that publishes events (`documents`, `identity`,
`agent`, and any service that sends `notifications.requests`).

| Metric | Type | Unit | Attributes | Prometheus |
|---|---|---|---|---|
| `skein.outbox.pending` | observable up-down counter | `{row}` | `messaging.destination.name` (topic) | `skein_outbox_pending` |
| `skein.outbox.lag` | observable gauge | `s` | `messaging.destination.name` | `skein_outbox_lag_seconds` |
| `skein.outbox.relay.publish.duration` | histogram | `s` | `messaging.system` (`gcp_pubsub`), `messaging.destination.name`, `error.type` | `skein_outbox_relay_publish_duration_seconds_*` |

`skein.outbox.lag` is the age of the oldest unpublished row (zero when the
outbox is empty), read in the same query as `pending`. It is the number to
alert on: it grows whenever the relay is down, stuck or being refused. The
publish rate is the histogram's `_count`.

### Pub/Sub consumers

Emitted by every subscription handler.

| Metric | Type | Unit | Attributes | Prometheus |
|---|---|---|---|---|
| `skein.consumer.process.duration` | histogram | `s` | `messaging.system`, `messaging.destination.subscription.name`, `skein.event.type` (payload kind: `blocks_changed`, ...), `skein.consumer.outcome`, `error.type` | `skein_consumer_process_duration_seconds_*` |
| `skein.consumer.event.age` | histogram | `s` | `messaging.system`, `messaging.destination.subscription.name` | `skein_consumer_event_age_seconds_*` |

`skein.consumer.outcome` is one of:

- `applied`: the effect and the processed-event record were committed.
- `duplicate`: the event ID was already processed, so the message was acked
  with no effect. This is normal after a relay crash (at-least-once delivery).
- `retry`: the handler failed and nacked the message for redelivery.
- `dead_lettered`: the handler failed on the last allowed delivery attempt
  (`delivery_attempt >= maxDeliveryAttempts`, which is 10 in
  `deploy/compose/pubsub-init.sh`), or rejected the message as permanently
  unprocessable, so Pub/Sub will move it to the `.dead-letter` topic.

The dead-letter count is the consumer's own view. Locally there is no other:
the Pub/Sub emulator exports no metrics, and it may not enforce dead-letter
policies or report delivery attempts the way production does, so a consumer
must not rely on the emulator for that logic. In GCP the authoritative numbers
are Pub/Sub's built-in metrics (see [GCP mapping](#gcp-mapping)).

`skein.consumer.event.age` is the time from `Event.occurred_at` to the start
of processing: end-to-end lag through the outbox, the relay, Pub/Sub and the
consumer's queue.

### Caches

Emitted by every in-process or Redis-backed cache (for example the gateway's
and services' `Authorize` cache, user profiles).

| Metric | Type | Unit | Attributes | Prometheus |
|---|---|---|---|---|
| `skein.cache.lookups` | counter | `{lookup}` | `skein.cache.name` (`authorize`, `user`, ...), `skein.cache.result` (`hit`, `miss`) | `skein_cache_lookups_total` |
| `skein.cache.invalidations` | counter | `{entry}` | `skein.cache.name`, `skein.cache.invalidation.reason` (`event`, `write`, `manual`) | `skein_cache_invalidations_total` |

Hits and misses are one counter with a `result` label rather than two
counters, so the hit ratio is a single selector and the two can never be
incremented inconsistently. Expiry by TTL is not counted: Redis does it
silently, and it shows up as misses. `event` means invalidated by a domain
event such as an access revocation, which is what keeps cached authorisation
correct.

### Rate limiter

Emitted by the gateway (GCRA in Redis) and by services' in-process token
buckets.

| Metric | Type | Unit | Attributes | Prometheus |
|---|---|---|---|---|
| `skein.ratelimit.decisions` | counter | `{request}` | `skein.ratelimit.layer` (`gateway`, `local`), `skein.ratelimit.budget` (`sync`, `search`, `agent`, `api`), `skein.ratelimit.subject` (`user`, `ip`), `skein.ratelimit.decision` (`allowed`, `limited`) | `skein_ratelimit_decisions_total` |

The limited key (a user ID or an IP address) is never a label. To find who is
being limited, use the gateway's `WARN` logs, which carry `user_id` or the IP.
Redis Insight (`make urls`) shows the live GCRA keys.

### Agent

Emitted by `agent`. Outcomes mirror `RunFinished.Outcome` in
`proto/skein/v1/agent.proto`.

| Metric | Type | Unit | Attributes | Prometheus |
|---|---|---|---|---|
| `skein.agent.run.duration` | histogram | `s` | `skein.agent.run.outcome` (`completed`, `cancelled`, `budget_exceeded`, `failed`) | `skein_agent_run_duration_seconds_*` |
| `skein.agent.runs.active` | up-down counter | `{run}` | none | `skein_agent_runs_active` |
| `skein.agent.run.steps` | histogram | `{step}` | `skein.agent.run.outcome` | `skein_agent_run_steps_*` |
| `skein.agent.step.duration` | histogram | `s` | `skein.agent.step.kind` (`model`, `tool`), `skein.agent.tool.name` (tool steps only), `error.type` | `skein_agent_step_duration_seconds_*` |
| `skein.agent.tokens` | counter | `{token}` | `skein.agent.token.type` (`input`, `output`), `skein.agent.model` | `skein_agent_tokens_total` |
| `skein.agent.budget.exhaustions` | counter | `{run}` | `skein.agent.budget.kind` (`steps`, `tokens`, `wall_time`) | `skein_agent_budget_exhaustions_total` |
| `skein.agent.confirmation.requests` | counter | `{confirmation}` | `skein.agent.tool.name` | `skein_agent_confirmation_requests_total` |
| `skein.agent.confirmation.wait.duration` | histogram | `s` | `skein.agent.tool.name`, `skein.agent.confirmation.decision` (`approved`, `rejected`, `expired`, `cancelled`) | `skein_agent_confirmation_wait_duration_seconds_*` |

Runs finished is the `_count` of `skein.agent.run.duration`; a separate
counter would only duplicate it. Run duration includes time paused for
confirmations. `skein.agent.tool.name` takes values from the fixed tool
registry, never from model output.

### Service metrics

Each service's own instruments, as the code defines them today (the
service READMEs say why each exists). The agent's metrics, the outbox's
pending and lag gauges, and ml's RPC metrics do not yet match the tables
above; [gaps.md](gaps.md) tracks those.

| Metric | Type | Unit | Attributes | Defined in |
|---|---|---|---|---|
| `skein.gateway.idempotency.outcomes` | counter | `{request}` | `skein.idempotency.outcome` | `services/gateway` |
| `skein.gateway.websocket.connections` | up-down counter | `{connection}` | `skein.websocket.endpoint` (`sync`, `agent`, `notifications`) | `services/gateway` |
| `skein.gateway.websocket.closes` | counter | `{connection}` | `skein.websocket.endpoint`, `skein.websocket.close_code` | `services/gateway` |
| `skein.documents.actors` | up-down counter | `{actor}` | none | `services/documents/internal/app/metrics.go` |
| `skein.documents.sync.sessions` | up-down counter | `{session}` | none | same |
| `skein.documents.ops.applied` | counter | `{op}` | `skein.batch.kind` | same |
| `skein.documents.batches.rejected` | counter | `{batch}` | `skein.reject.reason` | same |
| `skein.documents.actor.load.duration` | histogram | `s` | none | same |
| `skein.notifications.placed` | counter | `{notification}` | `skein.notification.kind`, `outcome` | `services/notifications/internal/app/producer.go` |
| `skein.notifications.suppressed` | counter | `{notification}` | `skein.notification.kind`, `reason` | same |
| `skein.notifications.post_commit.errors` | counter | `{error}` | `step` | same |
| `skein.notifications.emails` | counter | `{email}` | `outcome`, `type` | `services/notifications/internal/app/emailsender.go` |
| `skein.notifications.email.send.duration` | histogram | `s` | none | same |
| `skein.notifications.live.subscribers` | up-down counter | `{subscriber}` | none | `services/notifications/internal/app/live.go` |
| `skein.notifications.live.dropped` | counter | `{subscriber}` | none | same |
| `skein.ml.rpc.requests` | counter | `1` | `rpc.method`, `rpc.grpc.status_code` | `services/ml/src/skein_ml/adapters/observability.py` |
| `skein.ml.rpc.duration` | histogram | `ms` | `rpc.method`, `rpc.grpc.status_code` | same |
| `skein.ml.events` | counter | `1` | `kind`, `outcome` | same |
| `skein.ml.search.recall` | histogram | `1` | none | same |

### Histogram buckets

OpenTelemetry SDKs' default buckets (0, 5, 10, 25 ... 10 000) assume
milliseconds. With the unit in seconds, every sample would land in the first
bucket and every quantile panel would be meaningless. Set explicit boundaries
on every histogram (`metric.WithExplicitBucketBoundaries` in Go,
`explicit_bucket_boundaries_advisory` in Python, or a view):

| Histograms | Boundaries |
|---|---|
| RPC, HTTP, relay publish, consumer processing, agent steps | 0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10 (semantic conventions' advice for request durations) |
| `skein.consumer.event.age` | 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300 |
| `skein.agent.run.duration`, `skein.agent.confirmation.wait.duration` | 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300, 600 |
| `skein.agent.run.steps` | 1, 2, 3, 5, 8, 13, 21, 34, 55 |

Some instrumentation libraries already apply the request-duration advice to
their own metrics and some do not, depending on version. Check the exported
`le` labels once in the Prometheus UI rather than assuming.

## Traces and sampling

- Propagation is W3C Trace Context (`traceparent`) and Baggage, over HTTP
  headers, gRPC metadata and, for events, the `Event.traceparent` field
  (consumers start their span as a child of it, so a trace continues across
  Pub/Sub).
- The sampler is **parent-based**: a service follows its caller's sampling
  decision, so a trace is either complete or absent, never partial. At the
  root (the gateway, or a consumer whose event has no `traceparent`) it is a
  trace-ID ratio:

  ```
  OTEL_TRACES_SAMPLER=parentbased_traceidratio
  OTEL_TRACES_SAMPLER_ARG=1.0     # locally: every trace
  OTEL_TRACES_SAMPLER_ARG=0.05    # cloud: set per environment in the Kubernetes overlay
  ```

- Errors in unsampled traces still appear in metrics and logs; that is why
  RED comes from metrics and not from spans. If tail sampling (keep every
  error trace) is wanted later, it belongs in the collector, behind a load
  balancing exporter that routes by trace ID.
- Span names are low cardinality: the RPC method, the HTTP route template,
  `<subscription> process`. IDs go in attributes.

## Redaction

The collector drops credential-looking attributes from spans, span events,
metric data points, log records and resources before anything is exported, in
every environment: the GKE overlay reuses the same processor. A key is dropped
when its last dot-separated segment is, or contains as a `_`/`-`-separated
word, one of `password`, `passwd`, `secret`, `token`, `authorization`,
`cookie`, `credential(s)` or `api_key`/`apikey`. For example,
`http.request.header.authorization`, `refresh_token`, `user.password`,
`x-api-key` and `set-cookie` are dropped, while `skein.agent.token.type` and
`gen_ai.usage.input_tokens` are kept. `make stack-smoke` checks both lists
against all three backends.

This is a safety net. It cannot redact values inside log messages or span
names, so the logging rules above still apply.

## Dashboards

All three dashboards are provisioned from JSON in
`deploy/observability/grafana/dashboards`, into the Skein folder.

| Dashboard | Shows |
|---|---|
| Service overview | RED per service and per method or route (rate, server-fault ratio, p50/p95/p99), non-OK status codes, outgoing calls, cache hit ratio and invalidations, rate limiter decisions and limited ratio, recent warnings and errors from Loki |
| Outbox and Pub/Sub | Outbox lag and unpublished rows, relay publish rate, failures and latency, consumer outcomes, processing time, event age, dead letters, duplicate and retry ratios |
| Agent | Runs by outcome, active runs, run duration, steps per run, step and tool latency, tool errors, tokens per second and per run, budget exhaustion, confirmation requests, decisions and wait time |

The JSON files are the source of truth. Edit in Grafana if convenient, then
export the dashboard (Share, Export, without "export for sharing externally")
and commit it over the file. `make stack-check` validates the files, and
`make stack-smoke` runs every panel query against real data.

## API documentation

Swagger UI (`make urls`, port 8090) shows the gateway's REST API from
`gen/openapi/skein.swagger.json`, which the contracts pipeline generates from
`proto/` with grpc-gateway's OpenAPI generator. `gen/openapi` is mounted
read-only and served as static files, so a regenerated spec appears on a
browser refresh with no restart.

Until the contracts pipeline has produced the file, Swagger UI shows
"Failed to load API definition" with a 404 for `/openapi/skein.swagger.json`.
That is expected; the rest of the stack is unaffected. `make up` creates the
empty `gen/openapi` folder so Docker does not create it as root.

## Local stack reference

| Service | Image | URL | Notes |
|---|---|---|---|
| otel-collector | `otel/opentelemetry-collector-contrib:0.165.0` | OTLP `localhost:4317` (gRPC), `localhost:4318` (HTTP) | 512 MiB; memory_limiter at 80% of it |
| jaeger | `jaegertracing/jaeger:2.20.0` | http://localhost:16686 | Not newer: 2.21 removed the v1 query API that Grafana's Jaeger data source still uses for search |
| prometheus | `prom/prometheus:v3.16.0` | http://localhost:9090 | remote-write receiver and exemplar storage on |
| loki | `grafana/loki:3.7.10` | http://localhost:3100 | OTLP ingestion at `/otlp` |
| grafana | `grafana/grafana:13.3.1` | http://localhost:3000 | anonymous admin, local only |
| swagger-ui | `swaggerapi/swagger-ui:v5.35.0` | http://localhost:8090 | serves `gen/openapi` |
| redis-insight | `redis/redisinsight:3.8.0` | http://localhost:5540 | preconnected to the local Redis as `skein-local` |
| healthcheck-tools | `busybox:1.37.0-musl` | none | one-shot job; supplies `wget` to the collector and Loki images, which contain no shell |

The collector, Prometheus, Jaeger, Loki and Grafana are scraped by Prometheus
for their own health, so an empty panel can be told apart from a broken
pipeline: `otelcol_receiver_refused_*` (data the memory limiter turned away)
and `otelcol_exporter_send_failed_*` (which appears after the first failed
export) should stay at zero, and `otelcol_exporter_sent_*` should grow.

## GCP mapping

On GKE, every service keeps exporting OTLP to a collector. Only the
collector's exporters change, through the overlay
`deploy/observability/otel-collector.gke.yaml`, run on top of the base file:

```
otelcol-contrib --config=otel-collector.yaml --config=otel-collector.gke.yaml
```

The overlay adds GCP resource detection and Kubernetes metadata
(`k8s_attributes`) and swaps the exporters. The base file's memory limiter,
normalisation and redaction are reused unchanged, so what gets redacted is
defined once.

| Signal | Local | GKE | Exporter |
|---|---|---|---|
| Traces | Jaeger | Cloud Trace | `googlecloud` |
| Logs | Loki | Cloud Logging (log name `skein`), with `trace_id` and `span_id` mapped to the log entry's trace fields so Logs Explorer links to Cloud Trace | `googlecloud` |
| Metrics | Prometheus | Cloud Monitoring, through Google Cloud Managed Service for Prometheus | `googlemanagedprometheus` |

- **Why Managed Service for Prometheus for metrics.** It stores in Cloud
  Monitoring like the `googlecloud` exporter does, but keeps the exact
  Prometheus names and labels used here and answers PromQL. So the same
  Grafana dashboards work against it unchanged, and Cloud Monitoring's own
  charts and alerting policies can use PromQL too. The `googlecloud` exporter
  would write the metrics as `workload.googleapis.com/rpc.server.call.duration`
  and so on, which needs separate dashboards. Switching is one line in the
  overlay if that is preferred.
- **Deployment.** One collector Deployment per cluster (a gateway, not a
  sidecar), with a Service at `otel-collector.observability:4317` and an
  HPA. Services set `OTEL_EXPORTER_OTLP_ENDPOINT` to it. The collector's
  Kubernetes service account uses Workload Identity, bound to a Google
  service account with `roles/cloudtrace.agent`,
  `roles/monitoring.metricWriter` and `roles/logging.logWriter`; no key files.
  It also needs RBAC to get, list and watch pods and replicasets for
  `k8s_attributes`. The manifests belong in `deploy/k8s` and the bindings in
  `deploy/terraform`.
- **Logs are not collected twice.** GKE's built-in agent ships container
  stdout to Cloud Logging by default. Since services already export their
  logs over OTLP, the cluster's logging config enables only system
  components (`logging_config { enable_components = ["SYSTEM_COMPONENTS"] }`
  in Terraform); stdout stays available through `kubectl logs`. The trade-off
  is that logs written while the collector is unreachable reach Cloud Logging
  only after the SDK's retry. The alternative is the reverse: keep workload
  logging and drop the logs pipeline from the overlay.
- **Sampling.** `OTEL_TRACES_SAMPLER_ARG` is set per environment in the
  Kustomize overlays. Cloud Trace bills per span ingested, so production
  starts at about 5%.
- **Pub/Sub's own metrics** come with the platform and need no exporter:
  `pubsub.googleapis.com/subscription/num_undelivered_messages`,
  `oldest_unacked_message_age` (consumer backlog and lag) and
  `dead_letter_message_count` (the authoritative dead-letter count). They are
  queryable with PromQL in Managed Service for Prometheus as
  `pubsub_googleapis_com:subscription_num_undelivered_messages`, so they can
  sit next to the outbox panels.
- **Cloud SQL and Memorystore** report their own metrics the same way; the
  application-level cache and rate-limiter metrics above stay the ones to
  watch, because the platform cannot tell a hit from a miss.

## Verifying changes

| Command | Needs | Checks |
|---|---|---|
| `make stack-check` | Docker CLI; a Docker daemon, or `OTELCOL`, `JAEGER`, `PROMTOOL`, `LOKI` pointing to local binaries | `docker compose config` for each profile; dashboard JSON, UIDs and data sources; `otelcol validate` (base and GKE overlay), `jaeger validate`, `promtool check config`, `loki -verify-config` |
| `make stack-smoke` | a running `make up` | sends a minute of every metric above, a trace and logs with credential attributes; then every dashboard and variable query must return data, and redaction must have worked in Jaeger, Loki and Prometheus |
