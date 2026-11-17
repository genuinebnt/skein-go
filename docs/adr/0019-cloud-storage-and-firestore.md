# 0019. Cloud Storage and Firestore

Date: 2026-11-17. Status: accepted.

## Cloud Storage

| Bucket | Contents | Policy |
|---|---|---|
| `attachments` | Images and files embedded in notes, uploaded through signed URLs from documents | Private; served through signed URLs; object versioning |
| `event-archive` | Archived event-log segments (ADR-0016) | Retention lock; Archive storage class after 90 days |
| `backups` | Cloud SQL exports, CRDT snapshot archives | Lifecycle to Coldline; separate project for blast radius |
| `ml-artifacts` | Embedding model files, benchmark datasets, cluster snapshots | Versioned; read-only for the ml service |
| `web` | The built frontend, behind Cloud CDN | Public read through the load balancer only |
| `terraform-state` | Terraform state | Versioned, locked, admins only |

Attachments need a new contract (upload URL, attachment block type,
`AttachmentAdded` event), added with the attachments feature.

## Firestore

Firestore is not required: Postgres covers every current need. It is used
in one place where its model is a genuinely better fit, and as a learning
comparison:

- **Agent run traces** (messages, tool calls, results, token counts) are
  write-heavy, document-shaped, read by run ID, never joined, and should
  expire. A Firestore adapter for the agent's `RunStore` stores one document
  per run with a subcollection of steps and a TTL policy (90 days by
  default). Chats and LangGraph checkpoints stay in Postgres, because paused
  runs need transactional claims (ADR-0009 work in #16).
- The Postgres `RunStore` stays the default locally; the Firestore emulator
  is added to the stack for the adapter's tests.

## Consequences
- One more datastore to operate, scoped to one adapter behind an existing
  interface, so it can be removed without touching the agent's logic.
