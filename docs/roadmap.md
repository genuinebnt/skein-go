# Roadmap

What is done, what is in progress, and what comes next, in order. Each item
links the decision record that defines it. Items move to "Done" when their
pull request merges.

## Done
- Phase 1: libraries (`crdt`, `dp`, `graph`), contracts and generated code,
  `platform/`, the `ml` and `agent` services, observability, seed data.
- Phase 2: `gateway`, `identity`, `documents` with `libs/blocks`, `search`
  with `libs/trigram`, `notifications`; integration fixes; every service in
  the local stack.
- Phase 3: the web frontend (`web/`) in mock mode.

## In progress
- End-to-end run of the whole system natively (Postgres, Redis, the Pub/Sub
  emulator and all seven services), user-journey tests through the gateway,
  and fixes for what they find (`deploy/local/`, `e2e/`).

## Next, in order

1. **Contract gaps.** `AgentRunFinished` and `ConfirmationRequested` events
   published by the agent through an outbox (today nothing publishes
   `agent.events`); `CommentAdded` with comment RPCs; block-attribute writes
   and a sync acknowledgement frame; a CRDT restore operation for undo; email
   verification and password reset RPCs; run-usage fields; the share-link
   default expiry agreed between proto, docs and identity.
2. **Event store** ([ADR-0016](adr/0016-event-store.md)). Permanent event
   logs in each publishing service, the `ReadEvents` streaming RPC, catch-up
   subscribers in `platform/events`, checkpoints in every projection, and
   `make rebuild PROJECTION=...`.
3. **Service-to-service identity** ([ADR-0017](adr/0017-gcp-service-map-and-iam.md)).
   Google-signed ID tokens between services in GCP and a local equivalent,
   replacing the temporary internal-call workarounds.
4. **Terraform for ephemeral environments** ([ADR-0020](adr/0020-free-trial-profile.md),
   [ADR-0021](adr/0021-ephemeral-learning-environments.md)). The `trial`
   profile first: Cloud Run services and jobs scaled to zero, Cloud Run
   functions, Pub/Sub with dead letters, Cloud SQL, the e2-micro Redis VM,
   Cloud Storage, Firestore, Secret Manager, KMS, Artifact Registry,
   Scheduler, and IAM with Workload Identity and GitHub Workload Identity
   Federation. Cost guards: hourly estimate before `gcp-up`, session expiry
   labels with an auto-shutdown function, and a $30 budget. Commands:
   `make gcp-up`, `gcp-seed`, `gcp-e2e`, `gcp-cost`, `gcp-down` (verified
   complete) and `gcp-export`.
5. **Functions and storage features** ([ADR-0018](adr/0018-compute-gke-cloud-run-functions.md),
   [ADR-0019](adr/0019-cloud-storage-and-firestore.md)). Attachments through
   signed URLs with a processing function, dead-letter alerting and redrive,
   scheduled maintenance, event-log archiving, and the Firestore run-trace
   adapter.
6. **Kubernetes** ([ADR-0014](adr/0014-deployment.md),
   [ADR-0018](adr/0018-compute-gke-cloud-run-functions.md)). Kustomize
   manifests for the GKE-placed services, kind locally, and the `full`
   Terraform profile (GKE, Memorystore, load balancer, Cloud Armor,
   Certificate Manager), run for short sessions with a 4-hour default
   expiry (ADR-0021).
7. **Frontend against the live stack.** Run the Playwright suite against the
   native stack and then the trial deployment, and remove the mock-only
   fallbacks as their backend features land.
8. **Rust port** (owner). Libraries first, against the shared fixtures, per
   `docs/porting-to-rust.md`.

## Learning options, not scheduled
- A Kafka adapter behind `platform/events` next to Pub/Sub, comparing
  delivery, ordering and replay (ADR-0016).
- Product quantization and on-disk ANN in `ml`; GPU embeddings once the
  account is past the free trial.
