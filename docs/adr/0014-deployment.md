# 0014. Compose, kind, GKE with Terraform; no Ansible

Date: 2026-11-16. Status: accepted.

## Decision
- Local: Docker Compose (`deploy/compose`) with Postgres, Redis, the Pub/Sub
  emulator and Mailpit, plus the services as they are built.
- Local Kubernetes: kind with the same manifests (Kustomize overlays) used in
  the cloud.
- Cloud: GKE, Cloud SQL for Postgres, Memorystore for Redis, Pub/Sub, Secret
  Manager and Artifact Registry, all in Terraform modules. CI builds images,
  runs tests and applies Terraform plans.
- Ansible is not used. It configures long-lived machines; this system has
  none, because Kubernetes and Terraform declare everything. If self-hosting
  on plain VMs is wanted later, Ansible would provision those hosts.

## Consequences
- Every cloud dependency needs a local stand-in selected by configuration
  (emulator, MinIO for object storage, Mailpit for email).
