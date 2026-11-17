# 0017. GCP service map and IAM

Date: 2026-11-17. Status: accepted. Extends [0014](0014-deployment.md).

## GCP services and what they are for

| Service | Used for | Local stand-in |
|---|---|---|
| GKE | Stateful and long-lived services (ADR-0018) | Docker Compose, kind |
| Cloud Run | Stateless services and jobs (ADR-0018) | Docker Compose |
| Cloud Run functions | Event-driven glue (ADR-0018) | a local function runner |
| Pub/Sub | Live event delivery (ADR-0006, ADR-0016) | Pub/Sub emulator |
| Cloud SQL for PostgreSQL | One schema per service; event logs (ADR-0016) | Postgres |
| Memorystore for Redis | Caches, rate limits, presence, socket fan-out | Redis |
| Cloud Storage | Attachments, archives, backups, model files, web assets, Terraform state (ADR-0019) | MinIO or a local directory |
| Firestore | Agent run traces with expiry (ADR-0019) | Firestore emulator |
| Secret Manager | Database passwords, OAuth client secrets, the Anthropic key | `.env` |
| Cloud KMS | Identity's token-signing keys (asymmetric, never exported) | local key files |
| Artifact Registry | Container images | local builds |
| Cloud Load Balancing, Cloud Armor | HTTPS entry to the gateway, WAF and edge rate limits | none |
| Cloud DNS, Certificate Manager | Domain and managed TLS | none |
| Cloud Trace, Cloud Logging, Cloud Monitoring | Telemetry (docs/observability.md) | Jaeger, Loki, Prometheus |
| Cloud Scheduler | Scheduled jobs and functions | cron in the native runner |

## IAM

- **One Google service account per workload** (each service, each job,
  each function), bound to its Kubernetes service account through Workload
  Identity on GKE, or attached directly on Cloud Run. No service account
  keys exist anywhere.
- **Least privilege, on the resource, not the project:**
  - Pub/Sub: `roles/pubsub.publisher` on the service's own topics only;
    `roles/pubsub.subscriber` on its own subscriptions only.
  - Cloud SQL: `roles/cloudsql.client` plus IAM database authentication;
    each service account maps to its own database role, which owns only its
    schema (the same rule as `deploy/compose/postgres/init.sql`).
  - Secret Manager: `roles/secretmanager.secretAccessor` per secret, granted
    only to the service that needs it.
  - Cloud KMS: identity alone gets `roles/cloudkms.signerVerifier` on its
    signing key; others verify with the published public keys.
  - Cloud Storage: per-bucket roles; services write only under their own
    prefixes; the archive bucket is write-once (retention policy) for the
    event-log writer.
  - Firestore: `roles/datastore.user` for the agent only.
- **Service-to-service identity:** each call carries a Google-signed ID token
  for the caller's service account (audience = the target service), verified
  by `platform/grpcx`. This replaces the `IDENTITY_SERVICE_TOKEN` and
  "allow unauthenticated internal calls" workarounds, and lets identity
  authorize internal RPCs by caller. User tokens still travel separately.
- **CI and humans:** GitHub Actions deploys through Workload Identity
  Federation (OIDC, no keys), limited to the `main` branch and release tags.
  Engineers get read-only roles by default and elevate through time-bound
  grants. Terraform owns every IAM binding; nothing is granted by hand.
- **Guardrails (optional learning exercises, ADR-0021):** organisation
  policies forbidding service account keys, public buckets and external IPs
  on databases; VPC Service Controls around Pub/Sub, Cloud SQL, Storage and
  Firestore. Skein is a learning project, so these are documented and
  available, not required.

## Consequences
- Every new topic, bucket, secret or function needs its IAM binding in
  Terraform in the same change; CI fails a plan that grants a role at
  project level to a workload.
- Locally there is no IAM: the native runner and Compose use the emulators
  and a single local identity, so tests do not depend on GCP.
