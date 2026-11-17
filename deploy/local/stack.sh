# shellcheck shell=bash
# The native stack's building blocks: builds, infrastructure, the seven
# services' environments, seeding and the production-settings guard.
# Sourced by up.sh, restart.sh, seed.sh, setup.sh and prod-check.sh; set
# MODE (dev or prod) before calling anything that depends on it.
#
# Every service gets the environment of its compose file
# (deploy/compose/services/*.yml) with hostnames and ports translated, on
# top of an empty environment (run_service uses env -i), so nothing from the
# caller's shell leaks in except the variables named below.
#
# What differs between the modes, and why:
#
#   setting                  dev                          prod
#   builds                   go build, editable venvs     -trimpath -s -w, CGO off; non-editable venvs without dev extras
#   SKEIN_ENV                dev (gRPC reflection on)     prod (reflection off, signing keys required)
#   identity signing keys    generated at each start      generated once into .run/prod, kept across restarts
#   IDENTITY_DEV_SESSIONS    on (seed users sign in)      off
#   CORS                     localhost dev servers        SKEIN_PROD_LOCAL_CORS_ORIGINS, default none
#   telemetry                off unless SKEIN_NATIVE_OTEL_ENDPOINT   on, to the native collector (or that endpoint)
#   logs                     service defaults             info, set explicitly
#   gateway drain delay      0 s (fast restarts)          the 5 s default
#   PostgreSQL               fsync off                    durable defaults
#   seed                     loaded                       never (sign up through the API)
#
# Stopgaps that stay on in prod, until the roadmap item that removes each
# lands (listed by prod_stopgaps, printed by just prod-local):
#   - IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS=true: event consumers
#     call identity without a token until service-to-service identity
#     exists (docs/roadmap.md, "Service-to-service identity", ADR-0017).
#   - plaintext gRPC between services (GRPC_INSECURE, GRPC_CLIENT_INSECURE
#     default true): TLS comes from the platform in GCP.
#   - the agent's offline scripted model when ANTHROPIC_API_KEY is unset.
#   - local stand-ins: the Pub/Sub emulator (or Redis events), Mailpit for
#     SMTP, Postgres roles with the compose passwords and loopback trust.

# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

MODE="${MODE:-dev}"
# Recorded by up.sh so restart.sh and status.sh use the backend the running
# stack chose.
EVENTS_BACKEND_FILE="$RUN_DIR/events-backend"
EVENTS_BACKEND="$(cat "$EVENTS_BACKEND_FILE" 2>/dev/null || true)"

PG_URL_BASE="127.0.0.1:$SKEIN_NATIVE_PG_PORT/skein?sslmode=disable"
REDIS_BASE="redis://127.0.0.1:$SKEIN_NATIVE_REDIS_PORT"
PUBSUB_HOST="127.0.0.1:$SKEIN_NATIVE_PUBSUB_PORT"
JWKS_URL="http://127.0.0.1:$SKEIN_NATIVE_IDENTITY_ADMIN/.well-known/jwks.json"

require() { command -v "$1" >/dev/null 2>&1 || die "$1 is required: $2 (just doctor lists everything)"; }

# require_native_tools checks what starting the native stack needs.
require_native_tools() {
  require go "install Go 1.24 (just setup)"
  require redis-server "install Redis 7"
  require curl "used for health checks"
  require python3 "the ml and agent services need Python 3.12+"
  require uv "the Python services' environments are built with uv (just setup)"
  [ -n "$(pg_bin_dir)" ] || die "PostgreSQL 16 server binaries not found; set PG_BIN (just doctor)"
}

# --- builds -------------------------------------------------------------------

# build_go [SERVICE...]: build the named Go services (all by default) into
# MODE's bin directory. Dev also builds their seed loaders; prod builds
# release binaries the way the Dockerfiles do and no loaders.
build_go() {
  local -a wanted=("$@") pkgs=() flags=()
  [ ${#wanted[@]} -gt 0 ] || wanted=("${GO_SERVICES[@]}")
  local name
  for name in "${wanted[@]}"; do
    pkgs+=("./services/$name/cmd/$name")
    if [ "$MODE" = dev ] && [ -d "$REPO_ROOT/services/$name/cmd/$name-seed" ]; then
      pkgs+=("./services/$name/cmd/$name-seed")
    fi
  done
  [ "$MODE" = prod ] && flags=(-trimpath -ldflags=-s\ -w)
  mkdir -p "$(bin_dir "$MODE")"
  log "building ${wanted[*]} ($MODE) into $(bin_dir "$MODE")"
  (
    cd "$REPO_ROOT"
    [ "$MODE" = prod ] && export CGO_ENABLED=0
    go build ${flags[@]+"${flags[@]}"} -o "$(bin_dir "$MODE")/" "${pkgs[@]}"
  ) >"$LOG_DIR/build-go.log" 2>&1 || { cat "$LOG_DIR/build-go.log" >&2; die "go build failed"; }
}

# build_seed_loaders: the dev loaders, without rebuilding the services.
build_seed_loaders() {
  local -a pkgs=()
  local name
  for name in "${SEEDED_SERVICES[@]}"; do pkgs+=("./services/$name/cmd/$name-seed"); done
  (cd "$REPO_ROOT" && go build -o "$(bin_dir dev)/" "${pkgs[@]}") >"$LOG_DIR/build-seed.log" 2>&1 ||
    { cat "$LOG_DIR/build-seed.log" >&2; die "go build of the seed loaders failed"; }
}

# python_stamp SERVICE: a hash of what SERVICE's environment is built from.
# Dev environments are editable, so only the dependency files count; prod
# environments hold a copy of the code, so the sources count too.
python_stamp() {
  local svc="$1" dir="$REPO_ROOT/services/$1"
  {
    echo "$MODE"
    cat "$dir/pyproject.toml" "$REPO_ROOT/gen/python/pyproject.toml"
    [ -f "$dir/uv.lock" ] && cat "$dir/uv.lock"
    if [ "$MODE" = prod ]; then
      find "$dir/src" "$REPO_ROOT/gen/python/skein_proto" -type f -not -name '*.pyc' -print0 |
        sort -z | xargs -0 sha256sum
    fi
  } | sha256sum | cut -c1-16
}

# build_python [SERVICE...]: (re)build the ml and agent environments when
# what they are built from changed. The interpreter is the python3 on PATH
# (mise's pinned one after use_pinned_tools).
build_python() {
  local -a wanted=("$@")
  [ ${#wanted[@]} -gt 0 ] || wanted=(ml agent)
  local svc venv stamp python
  python="$(command -v python3)"
  mkdir -p "$LOG_DIR"
  for svc in "${wanted[@]}"; do
    venv="$(python_env "$MODE" "$svc")"
    stamp="$(python_stamp "$svc")"
    [ "$(cat "$venv/.skein-stamp" 2>/dev/null)" = "$stamp" ] && continue
    log "building the $svc environment ($MODE) in $venv"
    # uv pip is always given the environment's own interpreter: with only
    # VIRTUAL_ENV it could install into the base Python instead.
    (
      cd "$REPO_ROOT/services/$svc"
      case "$MODE/$svc" in
        dev/agent) UV_PROJECT_ENVIRONMENT="$venv" uv sync --python "$python" --frozen --extra postgres --extra dev ;;
        prod/agent) UV_PROJECT_ENVIRONMENT="$venv" uv sync --python "$python" --frozen --no-dev --extra postgres --no-editable ;;
        dev/ml) uv venv -q --allow-existing --python "$python" "$venv" &&
          uv pip install -q --python "$venv/bin/python" -e ../../gen/python -e ".[redis,postgres,pubsub,dev]" ;;
        prod/ml) uv venv -q --allow-existing --python "$python" "$venv" &&
          uv pip install -q --python "$venv/bin/python" --reinstall-package skein-ml --reinstall-package skein-proto \
            ../../gen/python ".[redis,postgres,pubsub]" ;;
      esac
    ) >"$LOG_DIR/build-$svc.log" 2>&1 || { cat "$LOG_DIR/build-$svc.log" >&2; die "$svc environment failed"; }
    echo "$stamp" >"$venv/.skein-stamp"
  done
}

# --- infrastructure -----------------------------------------------------------

psql_skein() {
  "$(pg_bin psql)" -h 127.0.0.1 -p "$SKEIN_NATIVE_PG_PORT" -U skein -d skein "$@"
}

start_postgres() {
  local data
  data="$(mode_dir "$MODE")/pgdata"
  if [ ! -f "$data/PG_VERSION" ]; then
    log "initialising PostgreSQL ($MODE) in $data"
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
EOF
    # Dev data is disposable; the production-like cluster keeps durability.
    if [ "$MODE" = dev ]; then
      printf '%s\n' "fsync = off" "synchronous_commit = off" "full_page_writes = off" >>"$data/postgresql.conf"
    fi
  fi
  if pg_running "$MODE"; then
    log "postgres already running"
  else
    port_free "$SKEIN_NATIVE_PG_PORT" || die "port $SKEIN_NATIVE_PG_PORT is taken"
    : >"$LOG_DIR/postgres.log"
    [ "$(id -u)" = 0 ] && chown postgres: "$LOG_DIR/postgres.log"
    as_postgres "$(pg_bin pg_ctl)" -D "$data" -l "$LOG_DIR/postgres.log" -w -t 60 start >/dev/null
    log "started postgres ($MODE) on $SKEIN_NATIVE_PG_PORT"
  fi
  ensure_database
  # init.sql creates one role and schema per service; run it once.
  if [ "$(psql_skein -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'identity'")" != 1 ]; then
    psql_skein -v ON_ERROR_STOP=1 -q -f "$REPO_ROOT/deploy/compose/postgres/init.sql"
    log "applied deploy/compose/postgres/init.sql"
  fi
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
  local fresh=0 data
  data="$(mode_dir "$MODE")/pubsub"
  is_running pubsub || fresh=1
  mkdir -p "$data"
  start pubsub gcloud beta emulators pubsub start --host-port="$PUBSUB_HOST" \
    --project=skein-local --data-dir="$data" --quiet
  wait_http pubsub "http://$PUBSUB_HOST" 90
  # The emulator keeps topics in memory, so a fresh one needs them again.
  if [ "$fresh" = 1 ]; then
    PUBSUB_API_BASE="http://$PUBSUB_HOST" sh "$REPO_ROOT/deploy/compose/pubsub-init.sh" \
      >"$LOG_DIR/pubsub-init.log" 2>&1 || { cat "$LOG_DIR/pubsub-init.log" >&2; die "pubsub-init failed"; }
    log "created topics and subscriptions (deploy/compose/pubsub-init.sh)"
  fi
}

start_mailpit() {
  install_mailpit
  start mailpit "$RUN_DIR/bin/mailpit" --smtp "127.0.0.1:$SKEIN_NATIVE_SMTP_PORT" \
    --listen "127.0.0.1:$SKEIN_NATIVE_MAILPIT_PORT" --smtp-auth-accept-any --smtp-auth-allow-insecure
  wait_http mailpit "http://127.0.0.1:$SKEIN_NATIVE_MAILPIT_PORT/readyz" 30
}

# start_otelcol: the native collector, unless the services export to an
# external one (SKEIN_NATIVE_OTEL_ENDPOINT).
start_otelcol() {
  [ -n "${SKEIN_NATIVE_OTEL_ENDPOINT:-}" ] && return 0
  install_otelcol
  run_service otelcol "$(printf '%s\n' \
      "SKEIN_NATIVE_OTLP_GRPC=$SKEIN_NATIVE_OTLP_GRPC" \
      "SKEIN_NATIVE_OTELCOL_METRICS=$SKEIN_NATIVE_OTELCOL_METRICS" \
      "SKEIN_NATIVE_OTELCOL_PROMETHEUS=$SKEIN_NATIVE_OTELCOL_PROMETHEUS")" \
    "$RUN_DIR/bin/otelcol" --config="$LOCAL_DIR/otelcol.yaml"
  wait_http otelcol "http://127.0.0.1:$SKEIN_NATIVE_OTELCOL_METRICS/metrics" 30
}

start_infrastructure() {
  start_postgres
  start_redis
  if [ "$EVENTS_BACKEND" = pubsub ]; then start_pubsub; fi
  start_mailpit
  if [ "$MODE" = prod ]; then start_otelcol; fi
}

# --- service environments -------------------------------------------------------

# skein_env is SKEIN_ENV for MODE: prod turns off reflection and the other
# dev conveniences in platform/config.
skein_env() { if [ "$MODE" = prod ]; then echo prod; else echo dev; fi; }

skein_version() {
  if [ "$MODE" = prod ]; then
    git -C "$REPO_ROOT" describe --always --dirty 2>/dev/null || echo unknown
  else
    echo dev
  fi
}

# otel_env: where telemetry goes. Prod always exports (to the native
# collector unless SKEIN_NATIVE_OTEL_ENDPOINT names another); dev exports
# only when an endpoint is given, since there is no collector natively.
otel_env() {
  local endpoint="${SKEIN_NATIVE_OTEL_ENDPOINT:-}"
  [ "$MODE" = prod ] && [ -z "$endpoint" ] && endpoint="http://127.0.0.1:$SKEIN_NATIVE_OTLP_GRPC"
  if [ -n "$endpoint" ]; then
    printf '%s\n' "OTEL_EXPORTER_OTLP_ENDPOINT=$endpoint" "OTEL_EXPORTER_OTLP_INSECURE=true"
    # A short push interval so prod-check sees every service's metrics within
    # seconds; the 60 s SDK default would make it wait a minute.
    [ "$MODE" = prod ] && printf '%s\n' "OTEL_METRIC_EXPORT_INTERVAL=5000"
  else
    printf '%s\n' "OTEL_SDK_DISABLED=true"
  fi
  return 0
}

# events_env: the backend choice (the contract with platform/events), plus
# the emulator address when it is Pub/Sub.
events_env() {
  printf '%s\n' "EVENTS_BACKEND=$EVENTS_BACKEND"
  [ "$EVENTS_BACKEND" = pubsub ] && printf '%s\n' "PUBSUB_EMULATOR_HOST=$PUBSUB_HOST"
  return 0
}

# Shared by every Go service: compose's platform variables, translated.
go_common_env() {
  local svc="$1"
  printf '%s\n' \
    "SKEIN_ENV=$(skein_env)" \
    "SKEIN_VERSION=$(skein_version)" \
    "PUBSUB_PROJECT=skein-local" \
    "OTEL_SERVICE_NAME=$svc" \
    "OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=$(local_environment_name)"
  events_env
  otel_env
}

local_environment_name() { if [ "$MODE" = prod ]; then echo prod-local; else echo local; fi; }

# signing_keys prints identity's IDENTITY_SIGNING_KEYS for prod: one active
# Ed25519 seed, generated on first use and kept (mode 600) so sessions
# survive restarts, as they would with Secret Manager.
signing_keys() {
  local file
  file="$(mode_dir prod)/identity-signing-keys"
  if [ ! -s "$file" ]; then
    mkdir -p "$(dirname "$file")"
    (umask 077 && printf 'active:%s' "$(random_secret)" >"$file")
  fi
  cat "$file"
}

env_identity() {
  go_common_env identity
  printf '%s\n' \
    "DATABASE_URL=postgres://identity:identity@$PG_URL_BASE" \
    "REDIS_URL=$REDIS_BASE/0" \
    "IDENTITY_GRPC_ADDR=127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
    "IDENTITY_ADMIN_ADDR=127.0.0.1:$SKEIN_NATIVE_IDENTITY_ADMIN" \
    "IDENTITY_OAUTH_REDIRECT_URIS=http://localhost:3000/auth/callback"
  if [ "$MODE" = prod ]; then
    printf '%s\n' \
      "IDENTITY_SIGNING_KEYS=$(signing_keys)" \
      "IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS=true"
  else
    printf '%s\n' \
      "IDENTITY_FIXED_NOW=${IDENTITY_FIXED_NOW:-}" \
      "IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS=${IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS:-true}" \
      "IDENTITY_DEV_SESSIONS=true"
  fi
}

env_documents() {
  go_common_env documents
  printf '%s\n' \
    "DATABASE_URL=postgres://documents:documents@$PG_URL_BASE" \
    "REDIS_URL=$REDIS_BASE/1" \
    "DOCUMENTS_GRPC_ADDR=127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_GRPC" \
    "DOCUMENTS_ADMIN_ADDR=127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_ADMIN" \
    "IDENTITY_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
    "IDENTITY_JWKS_URL=$JWKS_URL"
}

env_search() {
  go_common_env search
  printf '%s\n' \
    "DATABASE_URL=postgres://search:search@$PG_URL_BASE" \
    "REDIS_URL=$REDIS_BASE/0" \
    "GRPC_ADDR=127.0.0.1:$SKEIN_NATIVE_SEARCH_GRPC" \
    "ADMIN_ADDR=127.0.0.1:$SKEIN_NATIVE_SEARCH_ADMIN" \
    "IDENTITY_ADDR=dns:///127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
    "IDENTITY_JWKS_URL=$JWKS_URL" \
    "ML_ADDR=" \
    "OTEL_EXPORTER_OTLP_PROTOCOL=grpc"
}

env_notifications() {
  go_common_env notifications
  printf '%s\n' \
    "DATABASE_URL=postgres://notifications:notifications@$PG_URL_BASE" \
    "REDIS_URL=$REDIS_BASE/0" \
    "GRPC_ADDR=127.0.0.1:$SKEIN_NATIVE_NOTIFICATIONS_GRPC" \
    "HTTP_ADDR=127.0.0.1:$SKEIN_NATIVE_NOTIFICATIONS_ADMIN" \
    "IDENTITY_GRPC_TARGET=dns:///127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
    "IDENTITY_JWKS_URL=$JWKS_URL" \
    "SMTP_ADDR=127.0.0.1:$SKEIN_NATIVE_SMTP_PORT" \
    "SMTP_FROM=Skein <notifications@skein.localhost>" \
    "APP_BASE_URL=http://localhost:3000"
}

# python_log_level_env NAME: prod sets the level explicitly, so a changed
# default cannot turn on debug logging unnoticed.
python_log_level_env() { [ "$MODE" = prod ] && printf '%s\n' "$1=INFO"; return 0; }

env_ml() {
  printf '%s\n' \
    "SKEIN_ML_ADDRESS=127.0.0.1:$SKEIN_NATIVE_ML_GRPC" \
    "SKEIN_ML_ADMIN_ADDRESS=127.0.0.1:$SKEIN_NATIVE_ML_ADMIN" \
    "SKEIN_ML_DATABASE_URL=postgresql://ml:ml@127.0.0.1:$SKEIN_NATIVE_PG_PORT/skein" \
    "SKEIN_ML_REDIS_URL=$REDIS_BASE/1" \
    "REDIS_URL=$REDIS_BASE/1" \
    "SKEIN_ML_PUBSUB_PROJECT=skein-local" \
    "SKEIN_ML_PUBSUB_SUBSCRIPTION=ml.documents-events" \
    "SKEIN_ML_MIGRATIONS_DIR=$REPO_ROOT/services/ml/migrations" \
    "SKEIN_ENV=$(skein_env)" \
    "SKEIN_VERSION=$(skein_version)" \
    "OTEL_SERVICE_NAME=ml" \
    "OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=$(local_environment_name)" \
    "PYTHONUNBUFFERED=1"
  python_log_level_env SKEIN_ML_LOG_LEVEL
  events_env
  otel_env
}

agent_model_backend() {
  local backend="${SKEIN_AGENT_MODEL_BACKEND:-}"
  if [ -z "$backend" ]; then
    if [ -n "${ANTHROPIC_API_KEY:-}" ]; then backend=anthropic; else backend=scripted; fi
  fi
  printf '%s' "$backend"
}

env_agent() {
  printf '%s\n' \
    "SKEIN_AGENT_ADDR=127.0.0.1:$SKEIN_NATIVE_AGENT_GRPC" \
    "SKEIN_AGENT_ADMIN_ADDR=127.0.0.1:$SKEIN_NATIVE_AGENT_ADMIN" \
    "SKEIN_AGENT_MODEL_BACKEND=$(agent_model_backend)" \
    "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}" \
    "SKEIN_AGENT_STORAGE=postgres" \
    "SKEIN_AGENT_POSTGRES_DSN=postgresql://agent:agent@127.0.0.1:$SKEIN_NATIVE_PG_PORT/skein" \
    "SKEIN_AGENT_REDIS_URL=$REDIS_BASE/2" \
    "REDIS_URL=$REDIS_BASE/2" \
    "SKEIN_AGENT_MIGRATIONS_DIR=$REPO_ROOT/services/agent/migrations" \
    "SKEIN_IDENTITY_ADDR=127.0.0.1:$SKEIN_NATIVE_IDENTITY_GRPC" \
    "SKEIN_DOCUMENTS_ADDR=127.0.0.1:$SKEIN_NATIVE_DOCUMENTS_GRPC" \
    "SKEIN_SEARCH_ADDR=127.0.0.1:$SKEIN_NATIVE_SEARCH_GRPC" \
    "SKEIN_ML_ADDR=127.0.0.1:$SKEIN_NATIVE_ML_GRPC" \
    "SKEIN_NOTIFICATIONS_ADDR=127.0.0.1:$SKEIN_NATIVE_NOTIFICATIONS_GRPC" \
    "SKEIN_ENV=$(skein_env)" \
    "SKEIN_VERSION=$(skein_version)" \
    "OTEL_SERVICE_NAME=agent" \
    "OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=$(local_environment_name)" \
    "PYTHONUNBUFFERED=1"
  python_log_level_env SKEIN_LOG_LEVEL
  events_env
  otel_env
}

env_gateway() {
  go_common_env gateway
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
    "TRUSTED_PROXIES="
  if [ "$MODE" = prod ]; then
    printf '%s\n' "CORS_ALLOWED_ORIGINS=${SKEIN_PROD_LOCAL_CORS_ORIGINS:-}"
  else
    printf '%s\n' \
      "CORS_ALLOWED_ORIGINS=http://localhost:3000,http://localhost:5173,http://localhost:$SKEIN_NATIVE_WEB_PORT,http://127.0.0.1:$SKEIN_NATIVE_WEB_PORT" \
      "GATEWAY_DRAIN_DELAY=0s"
    # Lets make e2e drive a 429 with a few requests; the default budgets
    # stay in force everywhere else. Word splitting is the point.
    # shellcheck disable=SC2086
    [ -n "${SKEIN_NATIVE_GATEWAY_EXTRA_ENV:-}" ] && printf '%s\n' $SKEIN_NATIVE_GATEWAY_EXTRA_ENV
  fi
  return 0
}

# service_command SERVICE: what to run, from MODE's builds.
service_command() {
  case "$1" in
    ml) printf '%s\n' "$(python_env "$MODE" ml)/bin/skein-ml" ;;
    agent) printf '%s\n' "$(python_env "$MODE" agent)/bin/python" -m skein_agent ;;
    *) printf '%s\n' "$(bin_dir "$MODE")/$1" ;;
  esac
}

# run_service NAME ENV_LINES CMD...: start CMD with exactly ENV_LINES on top
# of a minimal environment, so nothing from the caller's shell leaks in.
run_service() {
  local name="$1" envs="$2"; shift 2
  local -a vars=("PATH=$PATH" "HOME=$HOME" "LANG=C.UTF-8")
  while IFS= read -r line; do [ -n "$line" ] && vars+=("$line"); done <<<"$envs"
  start "$name" env -i "${vars[@]}" "$@"
}

# start_service SERVICE: start one of the seven and wait for its readiness.
start_service() {
  local svc="$1" envs
  local -a cmd=()
  envs="$("env_$svc")"
  while IFS= read -r line; do cmd+=("$line"); done < <(service_command "$svc")
  [ "$svc" = agent ] && log "agent model backend: $(agent_model_backend)"
  run_service "$svc" "$envs" "${cmd[@]}"
  wait_http "$svc" "$(readyz_url "$svc")"
}

# start_web: the web app's dev server in live mode, proxying /v1 to the
# gateway (web/vite.config.ts), so the browser stays same-origin.
start_web() {
  require pnpm "the web app needs pnpm (just setup)"
  [ -d "$REPO_ROOT/web/node_modules" ] || (cd "$REPO_ROOT/web" && pnpm install --frozen-lockfile) >"$LOG_DIR/build-web.log" 2>&1 ||
    { cat "$LOG_DIR/build-web.log" >&2; die "pnpm install failed"; }
  (cd "$REPO_ROOT/web" && sh wasm/build.sh --if-missing) >>"$LOG_DIR/build-web.log" 2>&1 ||
    { cat "$LOG_DIR/build-web.log" >&2; die "the web app's WebAssembly build failed"; }
  run_service web "VITE_GATEWAY=http://127.0.0.1:$SKEIN_NATIVE_GATEWAY_HTTP" \
    sh -c "cd '$REPO_ROOT/web' && exec pnpm exec vite --mode live --host 127.0.0.1 --port $SKEIN_NATIVE_WEB_PORT --strictPort"
  wait_http web "http://127.0.0.1:$SKEIN_NATIVE_WEB_PORT/" 90
}

# --- seed -------------------------------------------------------------------------

SEED_STAMP="$RUN_DIR/seed.stamp"
SEED_PASSWORD_FILE="$RUN_DIR/seed-password"

# seed_dir PROFILE: the dataset directory; medium, large and stress are
# generated into bin/seed/PROFILE (git-ignored) on first use, through the
# Makefile's seed target so generation is defined once.
seed_dir() {
  local profile="$1" dir
  case "$profile" in
    small) dir="$REPO_ROOT/testdata/seed/small" ;;
    medium|large|stress) dir="$REPO_ROOT/bin/seed/$profile" ;;
    *) die "unknown seed profile $profile (small, medium, large, stress)" ;;
  esac
  if [ ! -f "$dir/manifest.json" ]; then
    [ "$profile" = large ] && log "large is about 6.6 GB and takes 10 to 15 minutes to generate (docs/seed-data.md)"
    log "generating the $profile dataset into $dir"
    make -C "$REPO_ROOT" --no-print-directory seed PROFILE="$profile" SEED_OUT="$dir" >&2 || die "seed generation failed"
  fi
  printf '%s' "$dir"
}

# load_seed DIR [PROFILE]: load DIR through every service's seed loader. The
# loaders are idempotent, but documents replays every op batch, so the stamp
# records which datasets this database already holds.
load_seed() {
  local dir="$1" profile="${2:-custom}" want loader
  [ "$MODE" = dev ] || die "seed data is not loaded in prod-local: sign up through the API instead"
  want="$(sha256sum "$dir/manifest.json" | cut -c1-16)"
  if grep -qs " $want\$" "$SEED_STAMP" && [ "${SKEIN_NATIVE_RESEED:-0}" != 1 ]; then
    log "seed $profile already loaded (SKEIN_NATIVE_RESEED=1 loads it again)"
    return 0
  fi
  for loader in "${SEEDED_SERVICES[@]}"; do
    [ -x "$(bin_dir dev)/$loader-seed" ] || { build_seed_loaders; break; }
  done
  local -a extra=()
  # Gives every seed user a password (identity's loader; never logged), so
  # the web app can sign in as them. Generated for a fresh database only:
  # the loader does not change users it already loaded.
  if [ -n "${SKEIN_SEED_PASSWORD:-}" ]; then
    extra+=("SKEIN_SEED_PASSWORD=$SKEIN_SEED_PASSWORD")
  elif [ ! -s "$SEED_STAMP" ]; then
    [ -s "$SEED_PASSWORD_FILE" ] || (umask 077 && random_secret | cut -c1-24 >"$SEED_PASSWORD_FILE")
    extra+=("SKEIN_SEED_PASSWORD=$(cat "$SEED_PASSWORD_FILE")")
  fi
  for loader in "${SEEDED_SERVICES[@]}"; do
    log "loading $loader's share of $dir"
    env -i "PATH=$PATH" "HOME=$HOME" ${extra[@]+"${extra[@]}"} \
      "DATABASE_URL=postgres://$loader:$loader@$PG_URL_BASE" \
      "$(bin_dir dev)/$loader-seed" -dir "$dir" >"$LOG_DIR/seed-$loader.log" 2>&1 ||
      { tail -n 40 "$LOG_DIR/seed-$loader.log" >&2; die "$loader-seed failed"; }
  done
  echo "$profile $want" >>"$SEED_STAMP"
}

# --- the production-settings guard --------------------------------------------------

# prod_stopgaps prints the dev-era settings prod-local deliberately keeps,
# one per line, with the reason and what removes it.
prod_stopgaps() {
  cat <<EOF
IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS=true  event consumers call identity without a token until service-to-service identity lands (docs/roadmap.md item 3, ADR-0017)
plaintext gRPC between services (GRPC_INSECURE / GRPC_CLIENT_INSECURE default true)  TLS is the platform's job in GCP (ADR-0017)
events backend: $EVENTS_BACKEND (local stand-in for Cloud Pub/Sub)
SMTP to Mailpit, Postgres with compose passwords and loopback trust, no TLS at the gateway (GATEWAY_HSTS off)
EOF
  [ "$(agent_model_backend)" = scripted ] && echo "agent model backend: scripted (offline); set ANTHROPIC_API_KEY to use the real model"
  return 0
}

# check_prod_settings: fail loudly, before anything starts, if any dev-only
# setting would reach a service in prod mode. It inspects the final
# environment of every service (what run_service would pass) and the
# caller's dev-only knobs, and names each offending variable (never a
# value: some are secrets).
check_prod_settings() {
  local -a problems=()
  local svc line name value var
  for var in IDENTITY_FIXED_NOW SKEIN_NATIVE_GATEWAY_EXTRA_ENV SKEIN_SEED_PASSWORD SKEIN_NATIVE_RESEED SKEIN_NATIVE_SEED_DIR; do
    [ -n "${!var:-}" ] && problems+=("$var is set in your shell; it is a dev-only knob")
  done
  for svc in "${SERVICES[@]}"; do
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      name="${line%%=*}" value="${line#*=}"
      case "$name" in
        SKEIN_ENV) [ "$value" = prod ] || problems+=("$svc: SKEIN_ENV=$value, want prod") ;;
        OTEL_SDK_DISABLED) [ "$value" = true ] && problems+=("$svc: telemetry is disabled") ;;
        *_LOG_LEVEL) [ "$value" = INFO ] || problems+=("$svc: $name=$value, want INFO") ;;
        GATEWAY_DRAIN_DELAY) problems+=("$svc: $name overrides the production drain delay") ;;
        RATE_LIMIT_*) problems+=("$svc: $name overrides a production rate limit") ;;
        IDENTITY_FIXED_NOW|IDENTITY_KEY_ROTATION_INTERVAL) [ -n "$value" ] && problems+=("$svc: $name is dev-only") ;;
        CORS_ALLOWED_ORIGINS)
          case ",$value," in *localhost*|*127.0.0.1*) problems+=("$svc: CORS allows a local dev origin") ;; esac ;;
        IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS) ;; # a listed stopgap
        *DEV*|*_ALLOW_*|*INSECURE*|*REFLECTION*|*FAKE*|*MOCK*)
          # Anything that looks like a convenience flag and is not a listed
          # stopgap is refused, so a new dev flag cannot slip in unnoticed.
          [ "$name" = OTEL_EXPORTER_OTLP_INSECURE ] || problems+=("$svc: $name looks like a dev-only flag and is not a listed stopgap") ;;
      esac
    done < <("env_$svc")
  done
  if [ ${#problems[@]} -gt 0 ]; then
    printf '[native] error: prod-local refuses to start with dev settings on:\n' >&2
    printf '  - %s\n' "${problems[@]}" >&2
    exit 1
  fi
  log "production settings check passed: no dev flags beyond the listed stopgaps"
}

# --- summary ----------------------------------------------------------------------

# seed_owner prints the anchor workspace owner's ID and email from the small
# seed (cast_key "owner").
seed_owner() {
  python3 -c '
import json, sys
for line in open(sys.argv[1]):
    u = json.loads(line)
    if u.get("cast_key") == "owner":
        print(u["id"], u["email"])
        break
' "$REPO_ROOT/testdata/seed/small/users.jsonl"
}

print_summary() {
  {
    echo "[native] stack is up ($MODE, events: $EVENTS_BACKEND)"
    echo "  gateway        http://127.0.0.1:$SKEIN_NATIVE_GATEWAY_HTTP/v1   (API docs at /docs)"
    is_running web && echo "  web app        http://127.0.0.1:$SKEIN_NATIVE_WEB_PORT   (live mode, /v1 proxied to the gateway)"
    echo "  mailpit        http://127.0.0.1:$SKEIN_NATIVE_MAILPIT_PORT"
    echo "  postgres       postgres://skein@127.0.0.1:$SKEIN_NATIVE_PG_PORT/skein"
    echo "  redis          redis://127.0.0.1:$SKEIN_NATIVE_REDIS_PORT"
    [ "$EVENTS_BACKEND" = pubsub ] && echo "  pubsub         PUBSUB_EMULATOR_HOST=$PUBSUB_HOST"
    if [ "$MODE" = prod ] && [ -z "${SKEIN_NATIVE_OTEL_ENDPOINT:-}" ]; then
      echo "  telemetry      OTLP 127.0.0.1:$SKEIN_NATIVE_OTLP_GRPC; service metrics http://127.0.0.1:$SKEIN_NATIVE_OTELCOL_PROMETHEUS/metrics"
    fi
    echo "  logs           just logs [service]   ($LOG_DIR)"
    if [ "$MODE" = dev ] && [ -s "$SEED_STAMP" ]; then
      local owner_id owner_email
      read -r owner_id owner_email < <(seed_owner)
      echo "  sign in as     $owner_email (owner of the anchor workspace)"
      if [ -s "$SEED_PASSWORD_FILE" ]; then
        echo "                 password: just seed-password"
      fi
      echo "                 API token: curl -s -X POST http://127.0.0.1:$SKEIN_NATIVE_IDENTITY_ADMIN/dev/sessions -d '{\"userId\":\"$owner_id\"}'"
    fi
    echo "  stop with      just stop"
  } >&2
}
