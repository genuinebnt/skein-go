# 0021. Ephemeral learning environments with a $30 cap

Date: 2026-11-17. Status: accepted. Refines [0020](0020-free-trial-profile.md); scopes [0017](0017-gcp-service-map-and-iam.md).

## Context
Skein is a learning project, not a production service. The owner will
deploy it to Google Cloud to practise, run tests and demos, and then shut
everything down. Small charges are acceptable, up to about $30 in total
beyond any free-trial credit. Nothing needs to stay up between sessions,
and no user data needs to survive a teardown.

## Decision
- **Everything is ephemeral.** `make gcp-up PROFILE=trial|full` creates the
  whole environment from Terraform, `make gcp-seed` loads the seed data,
  `make gcp-e2e` runs the end-to-end suite against it, and `make gcp-down`
  destroys every billable resource. Only the Terraform state bucket and the
  Artifact Registry repository survive between sessions (both near-free).
- **Both profiles are allowed.** `trial` (ADR-0020) is the cheap default for
  long sessions. `full` (GKE, Memorystore, load balancer, Cloud Armor,
  Certificate Manager) is for short sessions, run for a few hours to practise
  Kubernetes and the production-shaped setup, then destroyed.
- **Scale to zero by default.** Cloud Run services and jobs use minimum
  instances 0 except where a session needs warm instances (`gateway` during
  an end-to-end run). Cloud SQL is stopped when idle by the guard function.
- **Cost guards, in order:**
  1. Before `gcp-up`, the Makefile prints the estimated hourly cost of the
     chosen profile (Terraform plan plus a cost estimator) and asks for
     confirmation.
  2. Every resource carries `skein-session` and `expires-at` labels.
     `gcp-up` sets the expiry (default 4 hours for `full`, 24 hours for
     `trial`); a Cloud Scheduler job runs a function every 15 minutes that
     scales Cloud Run to zero, stops Cloud SQL and deletes the GKE cluster
     once the expiry passes.
  3. A project budget of $30 with alerts at 50, 80 and 100 percent; the
     100 percent alert triggers the same shutdown function.
  4. `make gcp-cost` shows month-to-date spend from the billing export.
- **Production hardening is optional.** High availability, point-in-time
  recovery, VPC Service Controls, organisation policies and multi-region
  are documented in ADR-0017 as learning exercises, not requirements, and
  are off by default. Workload Identity, least-privilege IAM, secrets in
  Secret Manager and no service account keys stay on, because they cost
  nothing and are the point of the exercise.
- **Data is disposable.** No backups are kept by default; `make gcp-export`
  exists for when a session's data is worth keeping. Seed data recreates a
  realistic workspace in minutes.

## Consequences
- A session costs only the hours it runs. Most of the managed services
  (Cloud Run, Pub/Sub, Firestore, Cloud Storage, functions) scale to zero
  or sit within the free tier, so the cost of a session is dominated by
  Cloud SQL, and in the `full` profile by GKE nodes, Memorystore and the
  load balancer.
- Teardown must be complete and verified: `gcp-down` ends by listing any
  remaining billable resources in the project and fails if there are any.
- The same Terraform and the same code serve both profiles; nothing in the
  services knows which one it runs in.
