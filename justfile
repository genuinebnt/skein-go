# Skein's front door. `just` lists every recipe by group; docs/dev-workflow.md
# explains them and the usual loops. Recipes are thin: the logic lives in
# deploy/local/*.sh and the Makefile, which keep working on their own.
#
# Every line runs through deploy/local/with-tools.sh, which puts the tools
# pinned in mise.toml first on PATH even when mise is not activated in your
# shell.

set shell := ["bash", "deploy/local/with-tools.sh", "-c"]
set quiet

# List every recipe
default:
    just --list --unsorted

# --- setup ----------------------------------------------------------------------

# Install the pinned tools (mise), web and Python dependencies, then run doctor
[group('setup')]
setup:
    deploy/local/setup.sh

# Check every tool, version, the Pub/Sub emulator, Postgres, Redis, Docker and free ports
[group('setup')]
doctor:
    deploy/local/doctor.sh

# --- local stack ----------------------------------------------------------------

# Start the stack and load the small seed: `just dev` (native), `just dev web` (plus the web app), `just dev docker` (compose)
[group('stack')]
dev target="native":
    case "{{ target }}" in \
      native) deploy/local/up.sh ;; \
      web) deploy/local/up.sh --web ;; \
      docker) deploy/local/docker.sh up ;; \
      *) echo "just dev [native|web|docker]" >&2; exit 2 ;; \
    esac

# Load a seed dataset into the running dev stack: small (default), medium, large or stress
[group('stack')]
seed profile="small":
    deploy/local/seed.sh {{ profile }}

# Drop the dev stack's data and start again with a fresh seed (default small)
[group('stack')]
reset profile="small":
    deploy/local/down.sh --wipe dev
    deploy/local/up.sh --seed {{ profile }}

# Stop whatever runs (native dev, prod-local or docker); data is kept
[group('stack')]
stop:
    if [ "$(cat deploy/local/.run/mode 2>/dev/null)" = docker ]; then deploy/local/docker.sh down; fi
    deploy/local/down.sh

# Follow the logs of every process, or of one: `just logs gateway`
[group('stack')]
logs service="":
    deploy/local/logs.sh {{ service }}

# Rebuild and restart services natively, leaving the rest running: `just restart gateway`
[group('stack')]
restart +services:
    deploy/local/restart.sh {{ services }}

# Show the mode, events backend, every process's readiness and the seed loaded
[group('stack')]
status:
    deploy/local/status.sh

# Print the generated password of the dev seed users (for the web app's sign-in)
[group('stack')]
seed-password:
    cat deploy/local/.run/seed-password 2>/dev/null || { echo "no generated seed password: the dev data predates it or SKEIN_SEED_PASSWORD was set; just reset" >&2; exit 1; }

# Run the same system with production settings (release builds, telemetry on, no dev flags, no seed) and check it
[group('stack')]
prod-local:
    deploy/local/prod-local.sh

# Re-run the production-settings checks against a running prod-local stack
[group('stack')]
prod-check:
    deploy/local/prod-check.sh

# --- tests and code quality -----------------------------------------------------

# Every test that needs no servers: Go (race detector), Python and web
[group('test')]
test: test-go test-python test-web

# Go: every module's tests with the race detector
[group('test')]
test-go:
    make test

# Python: the ml and agent unit tests and the generated package's tests
[group('test')]
test-python:
    for svc in ml agent; do echo "== services/$svc"; (cd services/$svc && .venv/bin/python -m pytest -q -p no:cacheprovider); done
    echo "== gen/python" && .venv/bin/python -m pytest -q -p no:cacheprovider gen/python

# Web: the WebAssembly bridge tests, typecheck and Vitest
[group('test')]
test-web:
    cd web/wasm && go test -count=1 ./...
    cd web && pnpm typecheck && pnpm test

# Every integration test against the running native stack's Postgres, Redis and Pub/Sub
[group('test')]
test-integration:
    deploy/local/integration.sh

# The end-to-end journeys through the gateway (running dev stack): `just e2e`, `just e2e TestSync`
[group('test')]
e2e run="":
    make e2e RUN='{{ run }}'

# The web app's Playwright suite in mock mode (needs Playwright's browsers)
[group('test')]
web-e2e:
    cd web && pnpm e2e

# Benchmarks of every Go module
[group('test')]
bench:
    make bench

# Fuzz every target for a while (default 30s)
[group('test')]
fuzz time="30s":
    make fuzz FUZZTIME={{ time }}

# Lint everything: gofmt and vet, contracts, ruff, eslint and typecheck, shell scripts, this file
[group('quality')]
lint:
    make fmt-check vet
    make proto-check
    for svc in ml agent; do echo "== ruff services/$svc"; (cd services/$svc && .venv/bin/ruff check . && .venv/bin/ruff format --check .); done
    cd web && pnpm lint && pnpm typecheck
    shellcheck -x -P deploy/local deploy/local/*.sh
    just --fmt --check --unstable

# Format everything the formatters own: Go, contracts, Python, this file
[group('quality')]
fmt:
    gofmt -w libs services platform tools e2e web/wasm
    cd proto && buf format -w --path skein
    for svc in ml agent; do (cd services/$svc && .venv/bin/ruff format . && .venv/bin/ruff check --fix .); done
    just --fmt --unstable

# Regenerate Go, OpenAPI and Python from proto/ (pinned buf, plugins and grpcio-tools)
[group('quality')]
proto:
    PYTHON="$PWD/.venv/bin/python" make proto

# --- web app ----------------------------------------------------------------------

# The web app in mock mode: no backend needed (port 5173, WEB_PORT to change)
[group('web')]
web:
    deploy/local/web.sh mock

# The web app in live mode against the running native gateway (port 25173)
[group('web')]
web-live:
    deploy/local/web.sh live

# --- cloud sessions (ADR-0021; not built yet) ------------------------------------

# Create an ephemeral GCP environment: `just gcp-up trial` or `just gcp-up full`
[group('cloud')]
gcp-up profile="trial":
    p="{{ profile }}"; p="${p#profile=}"; case "$p" in trial|full) ;; *) echo "profile must be trial or full" >&2; exit 2 ;; esac; \
      echo "gcp-up ($p): the Terraform for ephemeral environments is not built yet (docs/roadmap.md, 'Terraform for ephemeral environments'; ADR-0020, ADR-0021)." >&2; exit 1

# Destroy every billable resource of the session and verify nothing is left
[group('cloud')]
gcp-down:
    echo "gcp-down: not built yet; it arrives with the Terraform for ephemeral environments (docs/roadmap.md, ADR-0021)." >&2; exit 1

# Show month-to-date spend from the billing export
[group('cloud')]
gcp-cost:
    echo "gcp-cost: not built yet; it arrives with the Terraform for ephemeral environments (docs/roadmap.md, ADR-0021)." >&2; exit 1

# Run the end-to-end suite against the cloud session
[group('cloud')]
gcp-e2e:
    echo "gcp-e2e: not built yet; it arrives with the Terraform for ephemeral environments (docs/roadmap.md, ADR-0021)." >&2; exit 1
