# notifications

The inbox, live push and email of Skein. Implements `NotificationService`
([proto](../../proto/skein/v1/notifications.proto),
[API](../../docs/api/notifications.md)) and consumes the domain events of the
other services. Go, one module, layout as in [services/README.md](../README.md).

```
cmd/notifications/        wiring: config, adapters, servers, shutdown
cmd/notifications-seed/   seed loader and rule check
internal/domain/          rules, mention grammar, preferences, digest planning, page tokens (pure)
internal/app/             producer, inbox, live hub, email sender; port interfaces
internal/email/           plain-text and HTML templates, MIME encoding
internal/adapters/        grpc, postgres, pubsub, redis, smtp, identity
internal/fakes/           in-memory ports with CheckInvariants
internal/seed/            dataset reading, the seed's permission policy, replay, comparison
migrations/               0001_init.sql (embedded)
```

## What triggers what

| Subscription | Payload | Notification |
|---|---|---|
| `notifications.requests` | `NotificationRequested` | its kind, to its recipient, dropped if located in a note the recipient cannot read |
| `notifications.agent-events` | `NotificationRequested` (actor = the run) | `AGENT_RUN_FINISHED` to the run's user only; envelope attribute `skein-agent-event=confirmation_requested` marks a confirmation request |
| `notifications.documents-events` | `BlocksChanged` | `MENTION` to each user newly `@mentioned` in a block (not in code), if they can read the note |
| | `NoteCreated`, `NoteRenamed`, `NoteTrashed`, `NoteRestored` | none; keeps the note projection (title, creator, workspace) |
| | `NotePurged` | erases the note's notifications, mention projection and pending email |
| `notifications.identity-events` | `AccessChanged` on a note | `SHARE` to a guest, `ACCESS_CHANGED` to a member or when access is revoked |
| | `AccessChanged` revoking a membership | `ACCESS_CHANGED` to the removed member |
| (no payload yet) | a new comment | `MENTION` to mentioned readers, `COMMENT` to the note's creator and the parent comment's author, one per user per comment |

The rules are those of [docs/seed-data.md](../../docs/seed-data.md#notification-rules),
written in `internal/domain/rules.go`. Nobody is notified of their own action
(agent runs excepted: the run's user is the recipient by definition).
`NoteTrashed` notifies nobody: there is no kind for it, and once a note is
trashed only owners and admins can read it, so the read check already keeps
new activity from reaching anyone else.

Readability is asked of identity's `Authorize` (`internal/adapters/identity`)
behind the `app.Authorizer` port, at the time of the event. The seed check uses
`seed.Policy` instead, which evaluates the seed's permission rules at any past
time and agrees with all 3,116 user rows of `truth/permissions/matrix.jsonl`
that concern the actions asked here.

## Deduplication

- **Per event:** every consumer records `(consumer, event ID)` in
  `processed_events` (platform `outbox.ProcessOnce`) in the transaction of the
  effect. A redelivery is acked with outcome `duplicate`.
- **Per cause:** a recipient has at most one notification per source (a
  comment, a note role, a membership, an agent run). A newer fact from the
  same source refreshes it (unread again, new text, one more occurrence); an
  older or equal one changes nothing.
- **Per burst:** mentions typed into blocks are diffed against the block's
  previous mentions, so editing around an existing `@ada` notifies once.
  A mention removed and retyped, or another block mentioning the same user,
  within 10 minutes (`NOTIFICATIONS_COALESCE_WINDOW`) by the same actor in the
  same note is absorbed into the unread notification (`occurrences` counts
  them). Identical requests (same recipient, kind, title and place) coalesce
  the same way. Comments, shares and access changes never coalesce: each one
  is a distinct thing to see.

## Delivery

- **Inbox:** `ListInbox` newest first by `(created_at, id)` with opaque cursor
  tokens bound to the `unreadOnly` filter; `unread` is the whole inbox's
  count.
- **Live:** `Subscribe` streams new inbox entries. A notification is published
  on Redis channel `notifications:live:<user>`; each replica subscribes to the
  channels of users connected to it. Best effort: the client loads
  `ListInbox` on connect and on reconnect. A client that falls behind (64
  buffered) is cut off with `RESOURCE_EXHAUSTED`.
- **Email:** when the preference asks for it, an email job is written in the
  notification's transaction and becomes due after 1 minute
  (`NOTIFICATIONS_DIGEST_DELAY`). When a recipient's oldest job is due, all of
  their pending jobs go out together: one email, or a digest of up to 50.
  Jobs whose notification was read in the app meanwhile are skipped. Failures
  retry with exponential backoff and jitter (30 s doubling to 1 h); a 5xx
  reply, a missing address or 8 failed attempts dead-letter the send (state
  `dead`, last error kept, metric `skein.notifications.emails{outcome="dead_lettered"}`).

### The email guarantee

An email is never lost: its job commits with its notification. It is sent
at least once, and **twice only if the process dies (or loses the database,
or hangs past its 2-minute lease) after the SMTP server accepted the message
and before the "sent" mark commits.** The re-send carries the same
`Message-ID`, which mail clients use to fold duplicates. Exactly-once is not
possible over SMTP, which has no idempotent submission; the window is kept
to one database round trip, and a late success from a sender whose lease
expired is still recorded, so it stops further retries. The randomised test
`TestEmailOutboxNeverLosesAndDuplicatesOnlyAfterACrashBetweenSendAndMark`
injects transient and permanent SMTP failures and crashes at every step and
checks exactly this.

## Caching (Redis, shared by replicas, failing open)

| Cache | Key | TTL | Invalidated by | Why |
|---|---|---|---|---|
| `unread` | user | 60 s | every commit that changes the count (new, refreshed, read, purged) | read on every `ListInbox`, changes rarely |
| `authz` | user/action/resource | 30 s | `AccessChanged` for the user, before it is processed | every event asks; revocation must be seen at once |
| `profile` | user | 10 min | TTL | names and addresses for titles and email |
| `handles` | workspace | 60 s | TTL | member list for `@handle` resolution |

## Preferences

Per kind, `inApp` and `email`. Defaults (as documented in the API): everything
in the app; email for mentions, shares and access changes, not for comments and
agent runs. Only overrides are stored. A kind with both off produces nothing; a
kind with only email produces an email-only notification that the inbox, the
unread count and live push never show.

## Configuration

| Variable | Default | |
|---|---|---|
| `DATABASE_URL`, `REDIS_URL` | required (platform) | |
| `EVENTS_BACKEND` | `pubsub` | `pubsub` (needs `PUBSUB_PROJECT`, and `PUBSUB_EMULATOR_HOST` locally), `redis` or `memory` ([docs/local-first.md](../../docs/local-first.md)) |
| `GRPC_ADDR`, `HTTP_ADDR` | `:9090`, `:8080` | gRPC API; admin `/healthz`, `/readyz` |
| `IDENTITY_GRPC_TARGET` | `dns:///identity:9090` | Authorize, GetUser, ListMembers |
| `IDENTITY_JWKS_URL` | `http://identity:8080/.well-known/jwks.json` | token verification |
| `IDENTITY_SERVICE_TOKEN` | unset | bearer token for calls made while handling events |
| `GRPC_CLIENT_INSECURE` | `true` | plaintext to identity inside the cluster |
| `SMTP_ADDR`, `SMTP_FROM` | `localhost:1025`, `Skein <notifications@skein.localhost>` | Mailpit locally |
| `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_REQUIRE_TLS`, `SMTP_HELO_NAME` | unset, unset, `false`, `localhost` | STARTTLS is used whenever offered |
| `APP_BASE_URL` | `http://localhost:3000` | links in emails |
| `NOTIFICATIONS_COALESCE_WINDOW`, `NOTIFICATIONS_DIGEST_DELAY` | `10m`, `1m` | |
| `NOTIFICATIONS_UNREAD_CACHE_TTL`, `NOTIFICATIONS_EMAIL_MAX_ATTEMPTS`, `NOTIFICATIONS_EMAIL_POLL_INTERVAL`, `NOTIFICATIONS_SUBSCRIBER_BUFFER`, `PUBSUB_MAX_DELIVERY_ATTEMPTS` | `1m`, `8`, `5s`, `64`, `10` | |

The gateway forwards the caller's bearer token; this service verifies it
itself and ignores `x-skein-user-id`.

## Metrics

Besides the platform's (RPC, cache, Postgres, Redis):
`skein.consumer.process.duration` and `skein.consumer.event.age`
([observability.md](../../docs/observability.md)), and
`skein.notifications.placed{kind,outcome}`,
`skein.notifications.suppressed{kind,reason}`,
`skein.notifications.post_commit.errors{step}`,
`skein.notifications.emails{outcome,type}`,
`skein.notifications.email.send.duration`,
`skein.notifications.live.subscribers`, `skein.notifications.live.dropped`.
Logs carry IDs and counts, never titles, bodies or block text.

## Running and testing

```
cd services/notifications
go test -race ./...                         # unit tests: no Docker, no network
go test -tags integration ./...             # adds Postgres via testcontainers
go test -run='^$' -fuzz=FuzzExtractHandles ./internal/domain
go run ./cmd/notifications-seed -dir ../../testdata/seed/small -check-only
DATABASE_URL=... go run ./cmd/notifications-seed -dir ../../testdata/seed/small
```

The seed loader checks the manifest, replays the dataset's events through the
real consumers into memory and requires the result to equal
`notifications.jsonl` (930 notifications from 1,217 events for `small`), then
loads `notifications.jsonl` as is (IDs and read flags kept, no email). The
dataset has no preferences, so everyone has the defaults.

Fixtures the Rust port must match: `testdata/conformance/notifications/`
(mention grammar, email digest planning).

## Proposed contract changes

Not made here (`proto/` is not this service's to change):

1. `CommentAdded { note_id, comment_id, parent_comment_id, parent_author_id,
   BlockRef location, body, repeated string mentions }` in `events.proto`,
   published by documents. Until then comments only reach this service
   through the seed loader.
2. `NOTIFICATION_KIND_AGENT_CONFIRMATION_NEEDED` (and explicit
   `AgentRunFinished` / `ConfirmationRequested` payloads on `agent.events`),
   instead of `AGENT_RUN_FINISHED` plus the `skein-agent-event` attribute.
3. `User.handle` in `identity.proto`, instead of deriving handles from email
   local parts.
4. `AccessChanged.role` (the new role), so a raise can be told from a lower
   without asking identity.
