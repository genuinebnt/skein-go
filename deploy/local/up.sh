#!/usr/bin/env bash
# Starts the whole Skein stack natively, without Docker (make up-native):
# PostgreSQL 16, Redis, the Pub/Sub emulator, Mailpit, and the seven
# services as plain processes on the ports in ports.env, then loads the
# small seed. Every service gets the environment its compose file sets
# (deploy/compose/services/*.yml), with hostnames and ports translated.
#
# State lives in deploy/local/.run (git-ignored): pgdata, logs, pids,
# binaries and virtualenvs. Running it again is safe: what is already
# running is left alone, and the seed loaders are idempotent.
#
# Differences from compose, all deliberate:
#   - OTEL_SDK_DISABLED=true unless SKEIN_NATIVE_OTEL_ENDPOINT is set, since
#     there is no collector natively.
#   - identity runs with IDENTITY_DEV_SESSIONS=true, the test-only session
#     minting endpoint the e2e suite uses for seed users (docs/local-native.md).
#   - the agent uses the offline scripted model unless ANTHROPIC_API_KEY is
#     set.
#
# docs/local-native.md explains ports, logs and troubleshooting.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SEED_DIR="${SKEIN_NATIVE_SEED_DIR:-$REPO_ROOT/testdata/seed/small}"
MAILPIT_VERSION=v1.33.1

PG_URL_BASE="127.0.0.1:$SKEIN_NATIVE_PG_PORT/skein?sslmode=disable"
REDIS_BASE="redis://127.0.0.1:$SKEIN_NATIVE_REDIS_PORT"
PUBSUB_HOST="127.0.0.1:$SKEIN_NATIVE_PUBSUB_PORT"
JWKS_URL="http://127.0.0.1:$SKEIN_NATIVE_IDENTITY_ADMIN/.well-known/jwks.json"

mkdir -p "$RUN_DIR" "$PID_DIR" "$LOG_DIR" "$BIN_DIR" "$VENV_DIR"

# --- tools --------------------------------------------------------------------

require() { command -v "$1" >/dev/null 2>&1 || die "$1 is required: $2"; }
require go "install Go 1.24"
require redis-server "install Redis 7"
require gcloud "install the Google Cloud CLI with the pubsub-emulator component"
require java "the Pub/Sub emulator needs a Java runtime"
require curl "used for health checks"
require python3 "the ml and agent services need Python 3.12+"
require uv "the Python services' environments are built with uv"
[ -f "$HOME/.skein-env" ] && . "$HOME/.skein-env"

# --- infrastructure -----------------------------------------------------------

start_postgres() {
  local data="$RUN_DIR/pgdata"
  if [ ! -f "$data/PG_VERSION" ]; then
    log "initialising PostgreSQL in $data"
    mkdir -p "$data"
    [ "$(id -u)" = 0 ] && chown postgres: "$data"
    chmod 700 "$data"
    # Trust on the loopback only: this cluster is a throwaway on 127.0.0.1,
    # and the services still send the passwords compose uses.
    as_postgres "$(pg_bin initdb)" -D "$data" -U skein --auth=trust \
      --encoding=UTF8 --locale=C.UTF-8 >"$LOG_DIR/initdb.log" 2>&1 ||
      { cat "$LOG_DIR/initdb.log" >&2; die "initdb failed"; }
    cat >>"$data/postgresql.conf" <<EOF
listen_addresses = '127.0.0.1'
port = $SKEIN_NATIVE_PG_PORT
unix_socket_directories = '$data'
max_connections = 300
shared_buffers = 128MB
fsync = off
synchronous_commit = off
full_page_writes = off
EOF
  fi
  if as_postgres "$(pg_bin pg_ctl)" -D "$data" status >/dev/null 2>&1; then
    log "postgres already running"
  else
    port_free "$SKEIN_NATIVE_PG_PORT" || die "port $SKEIN_NATIVE_PG_PORT is taken"
    : >"$LOG_DIR/postgres.log"
    [ "$(id -u)" = 0 ] && chown postgres: "$LOG_DIR/postgres.log"
    as_postgres "$(pg_bin pg_ctl)" -D "$data" -l "$LOG_DIR/postgres.log" -w -t 60 start >/dev/null
    log "started postgres on $SKEIN_NATIVE_PG_PORT"
  fi
  ensure_database
  # init.sql creates one role and schema per service; run it once.
  if [ "$(psql_skein -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'identity'")" != 1 ]; then
    psql_skein -v ON_ERROR_STOP=1 -q -f "$REPO_ROOT/deploy/compose/postgres/init.sql"
    log "applied deploy/compose/postgres/init.sql"
  fi
}

psql_skein() {
  "$(pg_bin psql)" -h 127.0.0.1 -p "$SKEIN_NATIVE_PG_PORT" -U skein -d skein "$@"
}

ensure_database() {
  # initdb creates only "postgres"; compose's POSTGRES_DB creates "skein".
  if ! "$(pg_bin psql)" -h 127.0.0.1 -p "$SKEIN_NATIVE_PG_PORT" -U skein -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname = 'skein'" | grep -q 1; then
    "$(pg_bin createdb)" -h 127.0.0.1 -p "$SKEIN_NATIVE_PG_PORT" -U skein skein
  fi
}

start_redis() {
  start redis redis-server --port "$SKEIN_NATIVE_REDIS_PORT" --bind 127.0.0.1 \
    --save "" --appendonly no --dir "$RUN_DIR"
  wait_port redis "$SKEIN_NATIVE_REDIS_PORT" 20
}

start_pubsub() {
  local fresh=0
  is_running pubsub || fresh=1
  mkdir -p "$RUN_DIR/pubsub"
  start pubsub gcloud beta emulators pubsub start --host-port="$PUBSUB_HOST" \
    --project=skein-local --data-dir="$RUN_DIR/pubsub" --quiet
  wait_http pubsub "http://$PUBSUB_HOST" 90
  # The emulator keeps topics in memory, so a fresh one needs them again.
  if [ "$fresh" = 1 ]; then
    PUBSUB_API_BASE="http://$PUBSUB_HOST" sh "$REPO_ROOT/deploy/compose/pubsub-init.sh" \
      >"$LOG_DIR/pubsub-init.log" 2>&1 || { cat "$LOG_DIR/pubsub-init.log" >&2; die "pubsub-init failed"; }
    log "created topics and subscriptions (deploy/compose/pubsub-init.sh)"
  fi
}

install_mailpit() {
  [ -x "$BIN_DIR/mailpit" ] && return 0
  local arch sum
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 sum=84d7fe375ebef25df26758984dc0d5d5e17d8c0734cb2e77be7873b14ab51716 ;;
    aarch64|arm64) arch=arm64 sum=3c5f076cb676d6980e47da030861c6d60f1f5614103dea93cccbcdfc43198e12 ;;
    *) die "no pinned Mailpit build for $(uname -m); put a mailpit binary in $BIN_DIR" ;;
  esac
  [ "$(uname -s)" = Linux ] || die "the pinned Mailpit download is for Linux; put a mailpit binary in $BIN_DIR"
  local tmp; tmp="$(mktemp -d)"
  log "downloading Mailpit $MAILPIT_VERSION ($arch)"
  curl -sSfL -o "$tmp/mailpit.tgz" \
    "https://github.com/axllent/mailpit/releases/download/$MAILPIT_VERSION/mailpit-linux-$arch.tar.gz"
  echo "$sum  $tmp/mailpit.tgz" | sha256sum -c --quiet - || die "Mailpit checksum mismatch"
  tar -xzf "$tmp/mailpit.tgz" -C "$tmp" mailpit
  install -m 0755 "$tmp/mailpit" "$BIN_DIR/mailpit"
  rm -rf "$tmp"
}

start_mailpit() {
  install_mailpit
  start mailpit "$BIN_DIR/mailpit" --smtp "127.0.0.1:$SKEIN_NATIVE_SMTP_PORT" \
    --listen "127.0.0.1:$SKEIN_NATIVE_MAILPIT_PORT" --smtp-auth-accept-any --smtp-auth-allow-insecure
  wait_http mailpit "http://127.0.0.1:$SKEIN_NATIVE_MAILPIT_PORT/readyz" 30
}

# --- builds -------------------------------------------------------------------

build_go() {
  log "building Go services and seed loaders into $BIN_DIR"
  (
    cd "$REPO_ROOT"
    go build -o "$BIN_DIR/" \
      ./services/identity/cmd/identity ./services/identity/cmd/identity-seed \
      ./services/documents/cmd/documents ./services/documents/cmd/documents-seed \
      ./services/search/cmd/search ./services/search/cmd/search-seed \
      ./services/notifications/cmd/notifications ./services/notifications/cmd/notifications-seed \
      ./services/gateway/cmd/gateway
  ) >"$LOG_DIR/build-go.log" 2>&1 || { cat "$LOG_DIR/build-go.log" >&2; die "go build failed"; }
}

# Each Python service gets its own environment; rebuilt when its lock or
# pyproject changes (the stamp holds their hash).
build_python() {
  local agent_stamp ml_stamp
  agent_stamp="$(cat "$REPO_ROOT/services/agent/uv.lock" "$REPO_ROOT/services/agent/pyproject.toml" | sha256sum | cut -c1-16)"
  if [ "$(cat "$VENV_DIR/agent.stamp" 2>/dev/null)" != "$agent_stamp" ]; then
    log "building the agent environment (uv sync)"
    (cd "$REPO_ROOT/services/agent" &&
      UV_PROJECT_ENVIRONMENT="$VENV_DIR/agent" uv sync --frozen --extra postgres --extra dev) \
      >"$LOG_DIR/build-agent.log" 2>&1 || { cat "$LOG_DIR/build-agent.log" >&2; die "agent environment failed"; }
    echo "$agent_stamp" >"$VENV_DIR/agent.stamp"
  fi
  ml_stamp="$(cat "$REPO_ROOT/services/ml/pyproject.toml" | sha256sum | cut -c1-16)"
  if [ "$(cat "$VENV_DIR/ml.stamp" 2>/dev/null)" != "$ml_stamp" ]; then
    log "building the ml environment (uv pip)"
    (cd "$REPO_ROOT/services/ml" && uv venv -q --allow-existing "$VENV_DIR/ml" &&
      VIRTUAL_ENV="$VENV_DIR/ml" uv pip install -q -e ../../gen/python -e ".[redis,postgres,pubsub,dev]") \
      >"$LOG_DIR/build-ml.log" 2>&1 || { cat "$LOG_DIR/build-ml.log" >&2; die "ml environment failed"; }
    echo "$ml_stamp" >"$VENV_DIR/ml.stamp"
  fi
}

# --- services -----------------------------------------------------------------

# Shared by every Go service: compose's platform variables, translated.
go_common_env() {
  local svc="$1"
  printf '%s\n' \
    "SKEIN_ENV=dev" \
    "PUBSUB_PROJECT=skein-local" \
    "PUBSUB_EMULATOR_HOST=$PUBSUB_HOST" \
    "OTEL_SERVICE_NAME=$svc" \
    "OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local"
  otel_env
}

otel_env() {
  if [ -n "${SKEIN_NATIVE_OTEL_ENDPOINT:-}" ]; then
    printf '%s\n' "OTEL_EXPORTER_OTLP_ENDPOINT=$SKEIN_NATIVE_OTEL_ENDPOINT"
  else
    printf '%s\n' "OTEL_SDK_DISABLED=true"
  fi
}

# run_service NAME ENV_LINES CMD...: start CMD with exactly ENV_LINES on top
# of a minimal environment, so nothing from the caller's shell leaks in.
run_service() {
  local name="$1" envs="$2"; shift 2
  local -a vars=("PATH=$PATH" "HOME=$HOME" "LANG=C.UTF-8")
  while IFS= read -r line; do [ -n "$line" ] && vars+=("$line"); done <<<"$envs"
  start "$name" env -i "${vars[@]}" "$@"
}

start_identity() {
  run_service identity "$(go_common_env identity
    printf '%s\n' \
      "DATABASE_URL=postgres://identity:identity@$PG_URL_BASE" \
      "REDIS_URL=$REDIS_BASE/0" \
      "IDENTITY_GRPC_ADDR=127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
      "IDENTITY_ADMIN_ADDR=127.0.0.1:$SKEIN_NATIVE_IDENTITY_ADMIN" \
      "IDENTITY_OAUTH_REDIRECT_URIS=http://localhost:3000/auth/callback" \
      "IDENTITY_FIXED_NOW=${IDENTITY_FIXED_NOW:-}" \
      "IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS=${IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS:-true}" \
      "IDENTITY_DEV_SESSIONS=true")" \
    "$BIN_DIR/identity"
  wait_http identity "http://127.0.0.1:$SKEIN_NATIVE_IDENTITY_ADMIN/readyz"
}

start_documents() {
  run_service documents "$(go_common_env documents
    printf '%s\n' \
      "DATABASE_URL=postgres://documents:documents@$PG_URL_BASE" \
      "REDIS_URL=$REDIS_BASE/1" \
      "DOCUMENTS_GRPC_ADDR=127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_GRPC" \
      "DOCUMENTS_ADMIN_ADDR=127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_ADMIN" \
      "IDENTITY_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
      "IDENTITY_JWKS_URL=$JWKS_URL")" \
    "$BIN_DIR/documents"
  wait_http documents "http://127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_ADMIN/readyz"
}

start_search() {
  run_service search "$(go_common_env search
    printf '%s\n' \
      "DATABASE_URL=postgres://search:search@$PG_URL_BASE" \
      "REDIS_URL=$REDIS_BASE/0" \
      "GRPC_ADDR=127.0.0.1:$SKEIN_NATIVE_SEARCH_GRPC" \
      "ADMIN_ADDR=127.0.0.1:$SKEIN_NATIVE_SEARCH_ADMIN" \
      "IDENTITY_ADDR=dns:///127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
      "IDENTITY_JWKS_URL=$JWKS_URL" \
      "ML_ADDR=" \
      "OTEL_EXPORTER_OTLP_PROTOCOL=grpc")" \
    "$BIN_DIR/search"
  wait_http search "http://127.0.0.1:$SKEIN_NATIVE_SEARCH_ADMIN/readyz"
}

start_notifications() {
  run_service notifications "$(go_common_env notifications
    printf '%s\n' \
      "DATABASE_URL=postgres://notifications:notifications@$PG_URL_BASE" \
      "REDIS_URL=$REDIS_BASE/0" \
      "GRPC_ADDR=127.0.0.1:$SKEIN_NATIVE_NOTIFICATIONS_GRPC" \
      "HTTP_ADDR=127.0.0.1:$SKEIN_NATIVE_NOTIFICATIONS_ADMIN" \
      "IDENTITY_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
      "IDENTITY_JWKS_URL=$JWKS_URL" \
      "SMTP_ADDR=127.0.0.1:$SKEIN_NATIVE_SMTP_PORT" \
      "SMTP_FROM=Skein <notifications@skein.localhost>" \
      "APP_BASE_URL=http://localhost:3000")" \
    "$BIN_DIR/notifications"
  wait_http notifications "http://127.0.0.1:$SKEIN_NATIVE_NOTIFICATIONS_ADMIN/readyz"
}

start_ml() {
  run_service ml "$(printf '%s\n' \
      "SKEIN_ML_ADDRESS=127.0.0.1:$SKEIN_NATIVE_ML_GRPC" \
      "SKEIN_ML_ADMIN_ADDRESS=127.0.0.1:$SKEIN_NATIVE_ML_ADMIN" \
      "SKEIN_ML_DATABASE_URL=postgresql://ml:ml@127.0.0.1:$SKEIN_NATIVE_PG_PORT/skein" \
      "SKEIN_ML_REDIS_URL=$REDIS_BASE/1" \
      "SKEIN_ML_PUBSUB_PROJECT=skein-local" \
      "SKEIN_ML_PUBSUB_SUBSCRIPTION=ml.documents-events" \
      "SKEIN_ML_MIGRATIONS_DIR=$REPO_ROOT/services/ml/migrations" \
      "PUBSUB_EMULATOR_HOST=$PUBSUB_HOST" \
      "SKEIN_ENV=local" \
      "OTEL_SERVICE_NAME=ml" \
      "OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local" \
      "PYTHONUNBUFFERED=1"
    otel_env)" \
    "$VENV_DIR/ml/bin/skein-ml"
  wait_http ml "http://127.0.0.1:$SKEIN_NATIVE_ML_ADMIN/readyz"
}

start_agent() {
  local backend="${SKEIN_AGENT_MODEL_BACKEND:-}"
  if [ -z "$backend" ]; then
    if [ -n "${ANTHROPIC_API_KEY:-}" ]; then backend=anthropic; else backend=scripted; fi
  fi
  log "agent model backend: $backend"
  run_service agent "$(printf '%s\n' \
      "SKEIN_AGENT_ADDR=127.0.0.1:$SKEIN_NATIVE_AGENT_GRPC" \
      "SKEIN_AGENT_ADMIN_ADDR=127.0.0.1:$SKEIN_NATIVE_AGENT_ADMIN" \
      "SKEIN_AGENT_MODEL_BACKEND=$backend" \
      "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}" \
      "SKEIN_AGENT_STORAGE=postgres" \
      "SKEIN_AGENT_POSTGRES_DSN=postgresql://agent:agent@127.0.0.1:$SKEIN_NATIVE_PG_PORT/skein" \
      "SKEIN_AGENT_REDIS_URL=$REDIS_BASE/2" \
      "SKEIN_AGENT_MIGRATIONS_DIR=$REPO_ROOT/services/agent/migrations" \
      "SKEIN_IDENTITY_ADDR=127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
      "SKEIN_DOCUMENTS_ADDR=127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_GRPC" \
      "SKEIN_SEARCH_ADDR=127.0.0.1:$SKEIN_NATIVE_SEARCH_GRPC" \
      "SKEIN_ML_ADDR=127.0.0.1:$SKEIN_NATIVE_ML_GRPC" \
      "SKEIN_NOTIFICATIONS_ADDR=127.0.0.1:$SKEIN_NATIVE_NOTIFICATIONS_GRPC" \
      "OTEL_SERVICE_NAME=agent" \
      "OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local" \
      "PYTHONUNBUFFERED=1"
    otel_env)" \
    "$VENV_DIR/agent/bin/python" -m skein_agent
  wait_http agent "http://127.0.0.1:$SKEIN_NATIVE_AGENT_ADMIN/readyz"
}

start_gateway() {
  run_service gateway "$(go_common_env gateway
    printf '%s\n' \
      "REDIS_URL=$REDIS_BASE/0" \
      "GATEWAY_HTTP_ADDR=127.0.0.1:$SKEIN_NATIVE_GATEWAY_HTTP" \
      "IDENTITY_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
      "DOCUMENTS_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_GRPC" \
      "DOCUMENTS_SYNC_ADDR=127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_GRPC" \
      "SEARCH_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_SEARCH_GRPC" \
      "ML_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_ML_GRPC" \
      "AGENT_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_AGENT_GRPC" \
      "NOTIFICATIONS_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_NOTIFICATIONS_GRPC" \
      "IDENTITY_JWKS_URL=$JWKS_URL" \
      "CORS_ALLOWED_ORIGINS=http://localhost:3000,http://localhost:5173" \
      "TRUSTED_PROXIES=" \
      "GATEWAY_DRAIN_DELAY=0s"
    # Lets make e2e drive a 429 with a few requests; the default budgets
    # stay in force everywhere else.
    [ -n "${SKEIN_NATIVE_GATEWAY_EXTRA_ENV:-}" ] && printf '%s\n' $SKEIN_NATIVE_GATEWAY_EXTRA_ENV
    true)" \
    "$BIN_DIR/gateway"
  wait_http gateway "http://127.0.0.1:$SKEIN_NATIVE_GATEWAY_HTTP/readyz"
}

# --- seed ---------------------------------------------------------------------

# The loaders are idempotent, but documents replays every op batch, so a
# stamp records which dataset was loaded into this database.
load_seed() {
  local stamp="$RUN_DIR/seed.stamp" want
  want="$(sha256sum "$SEED_DIR/manifest.json" | cut -c1-16)"
  if [ "$(cat "$stamp" 2>/dev/null)" = "$want" ] && [ "${SKEIN_NATIVE_RESEED:-0}" != 1 ]; then
    log "seed already loaded (SKEIN_NATIVE_RESEED=1 loads it again)"
    return 0
  fi
  local loader
  local -a extra=()
  # Gives every seed user this password (identity's loader; never logged).
  [ -n "${SKEIN_SEED_PASSWORD:-}" ] && extra+=("SKEIN_SEED_PASSWORD=$SKEIN_SEED_PASSWORD")
  for loader in identity documents search notifications; do
    log "loading $loader's share of $SEED_DIR"
    env -i "PATH=$PATH" "HOME=$HOME" ${extra[@]+"${extra[@]}"} \
      "DATABASE_URL=postgres://$loader:$loader@$PG_URL_BASE" \
      "$BIN_DIR/$loader-seed" -dir "$SEED_DIR" >"$LOG_DIR/seed-$loader.log" 2>&1 ||
      { tail -n 40 "$LOG_DIR/seed-$loader.log" >&2; die "$loader-seed failed"; }
  done
  echo "$want" >"$stamp"
}

# --- main ---------------------------------------------------------------------

main() {
  build_go
  build_python
  start_postgres
  start_redis
  start_pubsub
  start_mailpit
  # Dependency order from the compose files: identity first (keys and
  # Authorize), then the services that call it, the gateway last.
  start_identity
  start_documents
  start_search
  start_notifications
  start_ml
  start_agent
  start_gateway
  load_seed
  cat >&2 <<EOF
[native] stack is up
  gateway        http://127.0.0.1:$SKEIN_NATIVE_GATEWAY_HTTP/v1   (API docs at /docs)
  mailpit        http://127.0.0.1:$SKEIN_NATIVE_MAILPIT_PORT     (SMTP 127.0.0.1:$SKEIN_NATIVE_SMTP_PORT)
  postgres       postgres://skein@127.0.0.1:$SKEIN_NATIVE_PG_PORT/skein
  redis          redis://127.0.0.1:$SKEIN_NATIVE_REDIS_PORT
  pubsub         PUBSUB_EMULATOR_HOST=$PUBSUB_HOST
  logs           $LOG_DIR
  stop with      make down-native
EOF
}

main "$@"
