---
title: Observability conventions
author: sre
editors: admin
tags: observability, conventions, primer
created: 222
---
Every service emits traces, metrics and logs through OpenTelemetry, and every log line carries the trace ID of the request that produced it.

## Naming

- Metrics: `skein_<service>_<thing>_<unit>`, for example `skein_documents_apply_duration_seconds`
- Spans: `<service>.<operation>`, for example `documents.ApplyOps`
- Never put a user ID, note ID or note text in a metric label

Detailed rules: [[Metric cardinality budget]], [[Log schema]], [[Tracing across Pub/Sub]], [[SLOs and burn-rate alerts]], [[Dashboards we actually use]].

```yaml
exporters:
  otlp:
    endpoint: otel-collector:4317
processors:
  batch:
    timeout: 5s
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp]
```
