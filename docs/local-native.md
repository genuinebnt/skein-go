# Running Skein natively (no Docker)

`make up-native` runs the whole stack as plain processes on one machine:
PostgreSQL 16, Redis, the Pub/Sub emulator, Mailpit and the seven services,
on high ports, with the small seed loaded. It is the stack `make e2e` and
`make integration-native` run against, and it works where Docker does not
(CI runners without a daemon, sandboxes, laptops that would rather not).
The Docker stack (`make up`) is unchanged; both can run at once.

`just dev` is the same command through the front door;
[dev-workflow.md](dev-workflow.md) lists every recipe (`just status`,
`just restart`, `just logs`, `just seed`, `just prod-local` for the
production-settings variant) and the usual loops. This page covers what
runs, the ports and troubleshooting.

## Requirements

| Tool | Why | Notes |
|---|---|---|
| Go 1.24+ | builds the Go services and seed loaders | |
| PostgreSQL 16 server binaries | `initdb`, `pg_ctl`, `psql` | looked up in `/usr/lib/postgresql/16/bin` and Homebrew's `postgresql@16`; set `PG_BIN` otherwise |
| `redis-server` | caches, rate limits, live fan-out | 7.x |
| Google Cloud CLI with `pubsub-emulator`, and Java | the Pub/Sub emulator | `gcloud components install pubsub-emulator beta`; without it the stack uses `EVENTS_BACKEND=redis` once every service supports it ([dev-workflow.md](dev-workflow.md#the-events-backend)) |
| Python 3.12+ and `uv` | the `ml` and `agent` services | dev runs them from `services/<name>/.venv` (`just setup`); prod-local builds its own in `deploy/local/.run/prod/venv` |
| `curl` | health checks, the Mailpit download | |

Mailpit (v1.33.1, the version compose pins) is downloaded once into
`deploy/local/.run/bin` and checked against a pinned SHA-256 (Linux amd64
and arm64). Elsewhere, or offline, put a `mailpit` binary there yourself.

When run as root, PostgreSQL runs as the `postgres` system user (it refuses
to run as root).

## Commands

```
make up-native                 # build, start everything, load testdata/seed/small (about 20 s warm)
make e2e                       # the end-to-end suite (e2e/), about 70 s
make e2e RUN=TestSync          # some journeys only
make integration-native        # every integration test against this stack, with counts per module
deploy/local/restart.sh documents gateway   # rebuild and restart some services, keep the data
make down-native               # stop everything (data kept)
make down-native WIPE=1        # stop and delete the data and logs
SKEIN_NATIVE_RESEED=1 make up-native        # load the seed again (loaders are idempotent)
```

`up.sh` is safe to run again: anything already running is left alone, and
the seed is loaded once per dataset (a stamp records the manifest's hash).

## Ports

All bound to 127.0.0.1; every one can be overridden in the environment
(`deploy/local/ports.env`). A service's gRPC port is 291NN and its admin
port 281NN.

| What | Port | Variable |
|---|---|---|
| Gateway (REST, WebSockets, `/docs`) | 28080 | `SKEIN_NATIVE_GATEWAY_HTTP` |
| identity gRPC / admin (JWKS, probes, dev sessions) | 29101 / 28101 | `SKEIN_NATIVE_IDENTITY_*` |
| documents gRPC / admin | 29102 / 28102 | `SKEIN_NATIVE_DOCUMENTS_*` |
| search gRPC / admin | 29103 / 28103 | `SKEIN_NATIVE_SEARCH_*` |
| notifications gRPC / admin | 29104 / 28104 | `SKEIN_NATIVE_NOTIFICATIONS_*` |
| ml gRPC / admin | 29105 / 28105 | `SKEIN_NATIVE_ML_*` |
| agent gRPC / admin | 29107 / 28107 | `SKEIN_NATIVE_AGENT_*` |
| PostgreSQL (`postgres://skein@127.0.0.1:25432/skein`) | 25432 | `SKEIN_NATIVE_PG_PORT` |
| Redis | 26379 | `SKEIN_NATIVE_REDIS_PORT` |
| Pub/Sub emulator (`PUBSUB_EMULATOR_HOST=127.0.0.1:28085`) | 28085 | `SKEIN_NATIVE_PUBSUB_PORT` |
| Mailpit SMTP / web UI and API | 21025 / 28025 | `SKEIN_NATIVE_SMTP_PORT`, `SKEIN_NATIVE_MAILPIT_PORT` |
| Web app, live mode (`just dev web`, `just web-live`) | 25173 | `SKEIN_NATIVE_WEB_PORT` |
| OpenTelemetry Collector (prod-local): OTLP gRPC / own metrics / service metrics | 24317 / 28888 / 28889 | `SKEIN_NATIVE_OTLP_GRPC`, `SKEIN_NATIVE_OTELCOL_METRICS`, `SKEIN_NATIVE_OTELCOL_PROMETHEUS` |

## What runs, and how it differs from compose

Each service gets the environment of its compose file
(`deploy/compose/services/*.yml`) with hostnames and ports translated; the
database roles and schemas come from `deploy/compose/postgres/init.sql`,
the topics and subscriptions from `deploy/compose/pubsub-init.sh`. On
purpose:

- **No telemetry export** (`OTEL_SDK_DISABLED=true`): there is no collector.
  Set `SKEIN_NATIVE_OTEL_ENDPOINT=http://127.0.0.1:4317` to export to one.
- **identity serves dev sessions** (`IDENTITY_DEV_SESSIONS=true`, below).
- **The agent runs its offline model** unless `ANTHROPIC_API_KEY` is set
  (or `SKEIN_AGENT_MODEL_BACKEND` names a backend). The offline model
  answers that it is offline, except for two commands that drive the real
  tools: `/search <query>` (search, then an answer citing the first hit)
  and `/delete <note id>` (pauses for confirmation).
- PostgreSQL runs with `fsync=off`: the data is disposable.

State lives in `deploy/local/.run` (git-ignored): `pgdata/`, `logs/<name>.log`
(JSON logs, one file per process; the previous run's in `logs.previous/`),
`pids/`, `bin/` (service binaries, Mailpit, the collector), `mode`, and
`prod/` (prod-local's own data, binaries and virtualenvs).

## Signing in as a seed user

Seed users have no passwords. With `IDENTITY_DEV_SESSIONS=true`, identity's
admin port (never routed by the gateway) starts an ordinary session for any
existing user:

```
curl -s -X POST http://127.0.0.1:28101/dev/sessions -d '{"userId":"019b7892-1c00-75eb-b4c7-d9d89d6544c6"}'
```

The answer is a `SignInResponse`; use its `session.accessToken` against the
gateway. The variable is refused at startup outside `SKEIN_ENV=dev` or
`test`, and the route does not exist when it is off. User IDs are in
`testdata/seed/small/users.jsonl` (`cast_key` names the anchor cast:
`owner` is Mira, the anchor workspace's owner).

A fresh dev database also gives every seed user a generated password,
Argon2id-hashed by the identity loader; `just seed-password` prints it
(for the web app's sign-in). `SKEIN_SEED_PASSWORD=... just reset` chooses
it instead (the loader does not change users it already loaded, hence the
reset).

## Integration tests against this stack

Every integration test still defaults to testcontainers, and takes an
override to use a running server instead:

| Variable | Used by | What the test does with it |
|---|---|---|
| `SKEIN_TEST_POSTGRES_DSN` (an admin URL, e.g. `postgres://skein@127.0.0.1:25432/postgres?sslmode=disable`) | platform `postgres`, `outbox`; identity, documents, search, notifications | a fresh database per test (`platform/postgres/pgtest`), dropped afterwards |
| `SKEIN_TEST_REDIS_URL` | platform `redisx` | a per-run key prefix |
| `SKEIN_TEST_PUBSUB_EMULATOR_HOST` | platform `events` | a per-run project |
| `SKEIN_ML_TEST_DATABASE_URL`, `SKEIN_AGENT_TEST_DATABASE_URL` | ml, agent (`pytest -m integration`) | the tests truncate their own tables, so point them at a database of their own |

`make integration-native` sets all of them, gives each Python suite a fresh
database, runs only the tests defined in integration-tagged files and
prints the counts (also in `deploy/local/.run/integration-report.txt`).

## Troubleshooting

- **`port N is taken`**: something else listens there. Override the port
  (`SKEIN_NATIVE_PG_PORT=35432 make up-native`) or stop the other process.
  A stale stack from another checkout: `make down-native` there.
- **A service exits during startup**: `up.sh` prints the last 30 lines of
  its log; the full log is `deploy/local/.run/logs/<name>.log`.
- **`initdb: cannot be run as root`**: `up.sh` handles this with
  `runuser -u postgres`; that user must exist (Debian and Ubuntu packages
  create it) and be able to traverse the checkout's directories.
- **The emulator takes long to start**: the first start unpacks the
  emulator; later ones take a few seconds. `logs/pubsub.log` shows Java's
  output.
- **HTTP 503 right after a restart**: the gateway's gRPC channel to the
  restarted service is reconnecting with backoff; the body carries
  `RetryInfo`. Retry after a second.
- **Every token is rejected after restarting identity**: in dev identity
  generates its signing key at startup, so sessions do not survive its
  restart. Sign in again (seed users: a new dev session).
- **Code changed but the stack runs the old one**: `just restart <service>`
  (`deploy/local/restart.sh <service>`) rebuilds and restarts it; `just dev`
  alone leaves running processes alone.
- **The search graph looks wrong after upgrading past the link-title fix**:
  search's projection stored seed links as note IDs before; rebuild it
  (`make down-native WIPE=1 && make up-native`, or the procedure in
  `services/search/README.md`).
- **No email in Mailpit**: mentions email after the 1-minute digest delay
  (`NOTIFICATIONS_DIGEST_DELAY`), and not at all if the notification was
  read in the app first. The UI is http://127.0.0.1:28025.
- **Events end up dead-lettered**: subscriptions back off from 1 s to 60 s
  for 10 attempts (about four minutes); check the consuming service's log
  for `event not processed`.
- **The Mailpit download is refused**: put a `mailpit` binary (any recent
  release) in `deploy/local/.run/bin/` and run `make up-native` again.
