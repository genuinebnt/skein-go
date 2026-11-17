# 0020. A deployment profile that fits the Google Cloud free trial

Date: 2026-11-17. Status: accepted. Complements [0017](0017-gcp-service-map-and-iam.md) and [0018](0018-compute-gke-cloud-run-functions.md).

## Context
The first real deployment will run on a Google Cloud Free Trial account:
$300 of credit for 90 days, alongside the always-free tier. The trial forbids
GPUs and Marketplace products, and when the credit or the 90 days run out
the billing account closes and every resource is stopped, with a 30-day
grace period before deletion. The full design in ADR-0017 and ADR-0018 (GKE
nodes, Memorystore, a load balancer with Cloud Armor) would spend the credit
quickly. Facts checked on 2026-11-17 against
https://docs.cloud.google.com/free/docs/free-cloud-features and
https://docs.cloud.google.com/free.

## Decision
Terraform gets two profiles from the same modules: `trial` and `full`.
`trial` keeps every service and every GCP concept the project is meant to
teach, but chooses the always-free or cheapest option for each:

| Need | `full` profile | `trial` profile | Free-tier fit |
|---|---|---|---|
| All seven services | GKE plus Cloud Run (ADR-0018) | Cloud Run for all seven; `documents` and `search` pinned to one instance (max instances 1), which keeps one actor per note without consistent hashing | Cloud Run always-free requests and vCPU-seconds |
| Kubernetes practice | GKE Standard or Autopilot | One small GKE Autopilot cluster created on demand for exercises, then deleted; the cluster management fee is covered for one cluster | One Autopilot or zonal cluster free; pods use credit |
| Jobs and glue | Cloud Run jobs and functions | Same | Same free tier |
| Events | Pub/Sub | Same | 10 GB of messages a month free |
| Postgres | Cloud SQL, high availability | Cloud SQL, smallest shared-core instance, no high availability, public IP off, automatic backups on | Uses credit; the largest single cost, so it is sized first |
| Redis | Memorystore | Redis on the always-free e2-micro VM in us-central1 (caches and rate limits tolerate a restart; presence and fan-out reconnect) | e2-micro free in us-west1, us-central1, us-east1 |
| Object storage | Several buckets (ADR-0019) | Same buckets, Standard class, us-central1 | 5 GB-months free in those regions |
| Agent traces | Firestore | Same | 1 GB, 50k reads and 20k writes a day free |
| Images | Artifact Registry | Same; Go images are distroless and small, and Python images are slimmed | 0.5 GB free |
| Builds | GitHub Actions | Same (Cloud Build as an option) | Cloud Build 120 minutes a day free |
| Entry point | Load balancer, Cloud Armor, Certificate Manager | The gateway's own Cloud Run HTTPS URL, with Cloud Run domain mapping when a domain exists | Load balancers are never free |
| Telemetry | Cloud Trace, Logging, Monitoring | Same, with info-level logs only and 10% trace sampling | Monthly allotments for logging and monitoring |
| ML acceleration | GPU Cloud Run for embeddings | CPU only | GPUs are not allowed in the trial |
| Secrets, KMS, IAM, Scheduler | As in ADR-0017 | Same | Small usage-based costs |

Guardrails:
- **Region:** us-central1 for everything, so the always-free allowances
  apply. The trade-off is latency from India, which is fine for a
  demonstration.
- **Budget:** a $300 budget with alerts at 25, 50, 75 and 90 percent, and a
  target of under $100 a month so the credit lasts the full 90 days.
- **Spend cap:** budget notifications go to a Pub/Sub topic, and a Cloud Run
  function scales the services to zero and stops the Cloud SQL instance at
  the 90 percent alert, so the credit is never exhausted by accident.
- **Teardown:** `make gcp-down` destroys everything except the backup
  bucket, and `make gcp-export` copies Postgres dumps and the event-log
  archive to the local machine. Run both before day 85, because resources
  stop when the trial ends.
- **Cost check:** CI runs `terraform plan` with a cost estimate for the
  trial profile and fails if the monthly estimate exceeds the target.

## Consequences
- Deploying the trial profile exercises Cloud Run, functions, Pub/Sub,
  Cloud SQL, Cloud Storage, Firestore, IAM with Workload Identity, Secret
  Manager, KMS, Scheduler and the operations suite for real. GKE is
  practised on a short-lived cluster.
- The `full` profile remains the reference architecture; switching
  profiles is a Terraform variable, not a code change, because services
  read only configuration and cloud APIs that both profiles provide.
- After the trial, the always-free parts (Cloud Run, Pub/Sub, Firestore,
  Cloud Storage, the e2-micro VM) can keep a small demo running at low cost,
  with Cloud SQL as the main paid item.
