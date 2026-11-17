# Developer workflow

One short command each to install the tools, run Skein, seed it, run it
production-like, test it and tear it down. `just` is the front door; this
page lists every recipe and the loops you will use most.
[local-native.md](local-native.md) covers the native stack's ports,
internals and troubleshooting.

## From a fresh clone

```
curl https://mise.run | sh     # once per machine (https://mise.jdx.dev); add `eval "$(mise activate bash)"` (or zsh) to your shell rc
mise trust && mise install just   # just at the pinned version (or install just any other way)
just setup                     # pinned tools, web and Python dependencies, then just doctor
just dev                       # the whole stack natively, small seed loaded (about 40 s from empty data)
just e2e                       # the end-to-end journeys through the gateway
just stop                      # stop everything; the data is kept
```

`just doctor` names anything mise cannot install, and how to get it:
PostgreSQL 16 server binaries, `redis-server` 7, and for events either the
Google Cloud CLI's Pub/Sub emulator with a Java runtime, or nothing extra
once the Redis events backend has landed (below). Docker is optional, and
only `just dev docker` needs it.

## How the pieces fit

```
just <recipe>  ──┐
                 ├──▶ deploy/local/*.sh  (setup, doctor, up, down, restart, seed, status, logs, prod-local, ...)
make <target>  ──┘         │
                           └──▶ lib.sh (processes, modes, Postgres, downloads) + stack.sh (builds, environments, seed, prod guard)
```

- **`justfile`**: thin recipes, grouped in `just --list`. Each line runs
  through `deploy/local/with-tools.sh`, which puts the versions pinned in
  `mise.toml` first on `PATH` even when mise is not activated in your shell.
- **`mise.toml`**: Go, Python, Node, pnpm, uv, just, buf, shellcheck and
  the four protoc plugins, at the versions the Makefile, `buf.gen.yaml`,
  `web/package.json` and CI use. Its header lists every place that must
  change together.
- **`Makefile`**: keeps working on its own (`make up-native`, `make e2e`,
  `make test`, ...). Where a recipe and a target do the same thing, both
  call the same script.
- **`deploy/local/`**: the logic. State lives in `deploy/local/.run`
  (git-ignored).

## Recipes

### setup

| Recipe | What it does |
|---|---|
| `just setup` | `mise trust` and `mise install`, `go mod download`, `pnpm install`, the `ml` and `agent` virtualenvs (`services/<name>/.venv`, editable, with dev extras), the tools venv (`.venv`: grpcio-tools and the generated Python package), then `just doctor`. Safe to repeat. |
| `just doctor` | Checks every pinned tool and its version, PostgreSQL, Redis, the events backend (emulator, gcloud, Java, or Redis support in this checkout), the project environments, Playwright's browsers, Docker and every native port. `FAIL` lines block `just dev`; `warn` lines say what they affect. Exits 1 on any `FAIL`. |

### stack

| Recipe | What it does |
|---|---|
| `just dev` | The native stack in dev mode: PostgreSQL, Redis, the events backend, Mailpit and the seven services as processes on high ports, then the small seed. Prints the URLs and how to sign in. Running it again leaves what runs alone. |
| `just dev web` | The same, plus the web app in live mode on http://127.0.0.1:25173 (`/v1` proxied to the gateway). Sign in with the printed seed user and `just seed-password`. |
| `just dev docker` | The compose stack instead (`make up-app`, then the small seed). Needs a Docker daemon. |
| `just seed [small\|medium\|large\|stress]` | Loads a dataset into the running dev stack. Medium, large and stress are generated into `bin/seed/<profile>` first ([seed-data.md](seed-data.md) has sizes and times; large is about 6.6 GB). Each dataset loads once; `SKEIN_NATIVE_RESEED=1` forces it. |
| `just reset [profile]` | Deletes the dev data and starts again with a fresh seed (default small). |
| `just stop` | Stops whatever runs (native dev, prod-local or docker). Data is kept; `deploy/local/down.sh --wipe [dev\|prod\|all]` deletes it too. |
| `just logs [service]` | Follows every process's log, or one (`just logs gateway`). JSON, one file per process in `deploy/local/.run/logs`; the previous run's are in `logs.previous`. |
| `just restart <service>...` | Rebuilds the named services and restarts only them, in dependency order (`just restart gateway`, `just restart documents search`). A compile error leaves the old process serving. Restarting identity in dev invalidates sessions: it generates its signing key at startup. |
| `just status` | The mode, the events backend, every process with PID and readiness, binary overrides and the loaded seeds. Exits 1 if a recorded process is down or unready. |
| `just seed-password` | The generated password every dev seed user has (for the web app's sign-in). |
| `just prod-local` | The same system with production settings, then `just prod-check` (below). Stops a running dev stack first; the two modes keep separate data. |
| `just prod-check` | Re-runs the runtime checks against a running prod-local stack. |

### test

| Recipe | What it does |
|---|---|
| `just test` | Everything that needs no servers: `test-go` (every Go module, race detector), `test-python` (ml, agent, `gen/python`) and `test-web` (WebAssembly bridge tests, typecheck, Vitest). |
| `just test-integration` | Every integration test (Go tag `integration`, pytest marker `integration`) against the running native stack's Postgres, Redis and Pub/Sub, with counts per module. |
| `just e2e [pattern]` | The end-to-end journeys (`e2e/`) through the gateway of the running dev stack; `just e2e TestSync` runs some. |
| `just web-e2e` | The web app's Playwright suite in mock mode. |
| `just bench` | Benchmarks of every Go module. |
| `just fuzz [time]` | Every fuzz target for a while (default 30s). |

### quality

| Recipe | What it does |
|---|---|
| `just lint` | gofmt and vet, `proto-check` (buf format, lint, breaking), ruff for ml and agent, eslint and typecheck for the web app, shellcheck on `deploy/local`, and `just --fmt --check`. |
| `just fmt` | gofmt, buf format, ruff format and fix, `just --fmt`. |
| `just proto` | Regenerates Go, OpenAPI and Python from `proto/` with the pinned buf, plugins and grpcio-tools. Commit the result; CI fails on any diff. |

### web

| Recipe | What it does |
|---|---|
| `just web` | The web app in mock mode, no backend (http://localhost:5173; `WEB_PORT` changes it). |
| `just web-live` | The web app in the foreground in live mode against the running native gateway (port 25173). |

### cloud

`just gcp-up [trial|full]`, `just gcp-down`, `just gcp-cost` and `just gcp-e2e`
are placeholders: they print where the work is planned and exit 1. They
arrive with "Terraform for ephemeral environments" in
[roadmap.md](roadmap.md) ([ADR-0020](adr/0020-free-trial-profile.md),
[ADR-0021](adr/0021-ephemeral-learning-environments.md)).

## Typical loops

**Change a Go service.** Edit, `go test ./services/<name>/...`, then
`just restart <name>` and try it through the gateway (or `just e2e
<pattern>`). Unit tests need no stack.

**Change ml or agent.** `cd services/<name> && .venv/bin/python -m pytest`,
then `just restart <name>`. Dev environments are editable, so a restart
picks up the code; changed dependencies rebuild the environment
automatically.

**Change the web app.** `just web` for UI work against the built-in mock
API; `just dev web` (or `just dev`, then `just web-live`) to drive the real
backend. Vite reloads on save.

**Change a contract.** Edit `proto/skein/v1`, `just proto`, `just lint`
(`buf breaking` runs there), then `just restart` the services that changed.

**Start over.** `just reset` (or `just reset medium` for a bigger dataset).

**Before a pull request.** `just lint && just test`, then with a dev stack
up `just e2e` and `just test-integration`. If you touched configuration,
startup or anything production-only, `just prod-local` too.

**Port a service to Rust.** See "Swapping in another binary" below and
[porting/README.md](porting/README.md).

## Modes

The native stack runs in one mode at a time (they share ports), recorded in
`deploy/local/.run/mode`. Starting the other mode stops the running one
first. Dev and prod keep separate data (`.run/pgdata` and `.run/prod`), so
seed data never reaches the production-like database.

| Setting | `just dev` | `just prod-local` |
|---|---|---|
| builds | `go build`; editable venvs with dev extras | `-trimpath -ldflags=-s -w`, `CGO_ENABLED=0`; non-editable venvs without dev extras (as the Dockerfiles build) |
| `SKEIN_ENV` | `dev` (gRPC reflection on) | `prod` (reflection off, signing keys required) |
| identity signing keys | generated at each start | generated once into `.run/prod`, kept across restarts |
| `IDENTITY_DEV_SESSIONS` | on (seed users sign in) | off |
| CORS | the local dev servers | `SKEIN_PROD_LOCAL_CORS_ORIGINS`, default none |
| telemetry | off unless `SKEIN_NATIVE_OTEL_ENDPOINT` | on, to a native OpenTelemetry Collector (`deploy/local/otelcol.yaml`), or to that endpoint |
| logs | service defaults | `INFO`, set explicitly |
| gateway drain delay | 0 s | the 5 s default |
| PostgreSQL | `fsync=off` | durable defaults |
| seed | loaded | never; sign up through the API |

**The guard.** Before anything starts, `just prod-local` builds every
service's final environment and refuses to start, naming each variable,
if a dev-only setting is in it: `SKEIN_ENV` not `prod`, telemetry disabled,
a non-`INFO` log level, a drain-delay or rate-limit override, dev-only
identity settings, a CORS origin on localhost, a dev knob in your shell
(`IDENTITY_FIXED_NOW`, `SKEIN_NATIVE_GATEWAY_EXTRA_ENV`,
`SKEIN_SEED_PASSWORD`, `SKEIN_NATIVE_RESEED`), or any variable that looks
like a convenience flag (`*DEV*`, `*_ALLOW_*`, `*INSECURE*`,
`*REFLECTION*`, `*FAKE*`, `*MOCK*`) and is not a listed stopgap.

**The stopgaps.** These stay on in prod-local, each until the roadmap item
that removes it. `just prod-local` prints them every time:

- `IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS=true`: event consumers
  call identity without a token until service-to-service identity lands
  ([roadmap.md](roadmap.md) item 3, ADR-0017).
- plaintext gRPC between services (`GRPC_INSECURE` and
  `GRPC_CLIENT_INSECURE` default true): TLS is the platform's job in GCP.
- local stand-ins: the Pub/Sub emulator (or Redis events) for Cloud
  Pub/Sub, Mailpit for SMTP, Postgres with the compose passwords and
  loopback trust, no TLS at the gateway.
- the agent's offline scripted model when `ANTHROPIC_API_KEY` is unset.

**The runtime checks** (`just prod-check`): every service is ready;
identity's dev-session route answers 404; gRPC reflection is off on the Go
services; the gateway grants no CORS to the Vite dev origin; a fresh
account signs up through the gateway, its token works, and it creates a
workspace and reaches ml and agent; every service's metrics reach the
collector and spans arrive; nothing logged at debug level.

One known gap is reported as `gap` instead of failing: the agent records
run metrics only and no `rpc.server.call.duration`
([observability.md](observability.md)), so an idle agent exports no
metrics. The entry lives in `known_metrics_gap` in
`deploy/local/prod-check.sh`; once the agent exports, the check says to
remove it.

## The events backend

Services choose their events backend with `EVENTS_BACKEND=pubsub|redis|memory`
(default `pubsub`; [local-first.md](local-first.md)). The runner sets it
for every service:

1. `SKEIN_EVENTS_BACKEND` if you set it (`pubsub` or `redis`);
2. otherwise `pubsub` when the emulator is installed (gcloud, its
   `pubsub-emulator` component, Java);
3. otherwise `redis`, when every service in this checkout supports it.

With Redis every service gets the same `EVENTS_REDIS_URL` and
`EVENTS_REDIS_DB=0`, because each service's `REDIS_URL` names its own cache
database. If neither works (no emulator, and a checkout from before the
Redis backend), `just dev` stops before starting anything and says what to
install; `just doctor` names the services that lack Redis support.
`memory` is refused for the stack: each process would get its own
in-process bus, and no event would cross services.

## Swapping in another binary

`SKEIN_BIN_<SERVICE>=path` runs `path` instead of that service's own build,
with exactly the environment, ports and readiness probe the built-in
service gets. This is how a Rust port joins the stack:

```
SKEIN_BIN_SEARCH=../skein-rs/target/release/search just dev     # start the stack with it
SKEIN_BIN_SEARCH=../skein-rs/target/release/search just restart search   # or swap it into a running stack
just status                                                     # search ... (runs /abs/path/search)
just e2e
SKEIN_BIN_SEARCH= just restart search                           # back to the Go build
```

Relative paths are relative to the repository root. The override is
recorded for the running stack, so `just restart` of that or any other
service keeps it; `just stop` forgets it. An overridden Go service is not
rebuilt, but its Go seed loader still fills the schema both
implementations share. It works in both modes, so a release build can run
under `just prod-local` too.

## Knobs

| Variable | Effect |
|---|---|
| `SKEIN_NATIVE_*_PORT`, `SKEIN_NATIVE_<SERVICE>_GRPC`/`_ADMIN`, ... | Every port ([deploy/local/ports.env](../deploy/local/ports.env)). |
| `SKEIN_EVENTS_BACKEND` | Force `pubsub` or `redis`. |
| `SKEIN_BIN_<SERVICE>` | Run another binary for one service. |
| `SKEIN_NATIVE_OTEL_ENDPOINT` | Export telemetry to that collector (for example the compose observability stack at `http://127.0.0.1:4317`). |
| `SKEIN_SEED_PASSWORD` | The seed users' password on a fresh dev database (generated otherwise; `just seed-password`). |
| `SKEIN_NATIVE_RESEED=1` | Load a dataset again. |
| `SKEIN_PROD_LOCAL_CORS_ORIGINS` | CORS origins for prod-local (no localhost ones). |
| `ANTHROPIC_API_KEY`, `SKEIN_AGENT_MODEL_BACKEND` | The agent's model (offline scripted model by default). |
| `PG_BIN` | Where the PostgreSQL 16 binaries are, if not in a usual place. |
| `LINES_BACK` | How many lines `just logs` starts with. |
