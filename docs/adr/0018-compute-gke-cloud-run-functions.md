# 0018. Compute: GKE, Cloud Run and Cloud Run functions

Date: 2026-11-17. Status: accepted. Refines [0014](0014-deployment.md).

## Context
ADR-0014 put everything on GKE. Some workloads hold state in memory or
keep connections open for hours; others are stateless request handlers or
short jobs that are cheaper and simpler on a serverless platform. Running
both is also part of what this project is meant to teach.

## Decision

| Workload | Runs on | Why |
|---|---|---|
| `gateway` | GKE | Long-lived WebSockets, consistent-hash routing of sync to documents instances |
| `documents` | GKE (StatefulSet) | One actor per open note in memory; stable instance identity for routing |
| `search` | GKE | In-memory trigram index and graph caches per replica, fed by the change log |
| `identity` | GKE | Hot path for every request; low latency, steady load |
| `notifications` | Cloud Run (min instances 1) | Stateless consumers and `Subscribe` streams within Cloud Run's request limits; scales with event volume |
| `ml` | Cloud Run (CPU, later GPU) | Bursty embedding and search load; scales to zero when idle |
| `agent` | Cloud Run | Stateless between steps (state in Postgres and Firestore); runs fit the 60-minute request limit |
| Migrations, seed loads, evals, benchmarks, projection rebuilds | Cloud Run jobs | Run to completion, on demand or scheduled |
| Attachment processing (text extraction, thumbnails) | Cloud Run function on Storage upload | Small, event-driven, independent |
| Dead-letter alerting and redrive | Cloud Run function on each `*.dead-letter` topic | Turns a dead letter into an alert and a one-click redrive |
| Scheduled maintenance (expired tokens, event-log archiving, betweenness recompute) | Cloud Run functions on Cloud Scheduler | Periodic, short |

- Cloud Run services join the VPC through Direct VPC egress to reach Cloud
  SQL and Memorystore privately; GKE and Cloud Run talk over internal
  addresses only.
- Functions are thin: they call service RPCs or publish events, and contain
  no domain logic, so they port to Rust with the services.
- Any service can move between GKE and Cloud Run: images, configuration and
  health endpoints are identical; only the Terraform module differs.

## Consequences
- Two deployment shapes to maintain in Terraform (GKE manifests and Cloud
  Run services), plus functions. Shared modules keep labels, IAM and
  telemetry identical.
- `Subscribe` streams on Cloud Run are bounded by its request timeout; the
  client already reconnects with backoff (docs/api/websocket.md).
