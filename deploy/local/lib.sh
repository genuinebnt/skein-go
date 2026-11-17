# shellcheck shell=bash
# Helpers shared by the scripts in deploy/local. Sourced, not executed.
#
# Every long-running process is started in its own session (setsid), so its
# process group ID equals the PID written to .run/pids/<name>.pid. Stopping
# signals the whole group: that matters for the Pub/Sub emulator, where
# gcloud starts a Java child that would otherwise outlive it, and for the web
# dev server, where pnpm starts node.
#
# Run modes. The native stack runs in one mode at a time, recorded in
# .run/mode, because both modes use the same ports:
#   dev    just dev, make up-native: debug builds, dev conveniences on, seed loaded
#   prod   just prod-local: release builds, production settings, no seed
#   docker just dev docker: the compose stack (nothing native runs)
# Each native mode keeps its own data (dev in .run, prod in .run/prod), so
# switching never mixes seed data into the production-like database.

LOCAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$LOCAL_DIR/../.." && pwd)"
RUN_DIR="$LOCAL_DIR/.run"
PID_DIR="$RUN_DIR/pids"
LOG_DIR="$RUN_DIR/logs"
MODE_FILE="$RUN_DIR/mode"

# The seven services in dependency order (identity first: keys and
# Authorize; the gateway last). Stopping goes in reverse.
# shellcheck disable=SC2034 # these are used by the scripts that source this file
SERVICES=(identity documents search notifications ml agent gateway)
# shellcheck disable=SC2034
GO_SERVICES=(identity documents search notifications gateway)
# Go services with a seed loader (cmd/<name>-seed).
# shellcheck disable=SC2034
SEEDED_SERVICES=(identity documents search notifications)
# Everything else up.sh may start, stopped after the services.
SUPPORT_PROCESSES=(web otelcol mailpit pubsub redis)

# shellcheck source=ports.env
. "$LOCAL_DIR/ports.env"

log() { printf '[native] %s\n' "$*" >&2; }
die() { printf '[native] error: %s\n' "$*" >&2; exit 1; }

# use_pinned_tools puts the versions from mise.toml first on PATH, so the
# scripts build with the pinned Go, uv and pnpm even when mise is installed
# but not activated in the caller's shell. Without mise the tools already on
# PATH are used (`just doctor` says whether their versions match).
# ~/.skein-env, when present, holds machine-specific settings such as a Go
# module proxy; it is loaded first so the pinned tools still win on PATH.
use_pinned_tools() {
  # shellcheck disable=SC1091
  [ -f "$HOME/.skein-env" ] && . "$HOME/.skein-env"
  if [ -z "${MISE_SHELL:-}" ] && command -v mise >/dev/null 2>&1; then
    local env_script
    if env_script="$(cd "$REPO_ROOT" && mise env -s bash 2>/dev/null)"; then
      eval "$env_script"
    fi
  fi
  return 0
}

# --- modes --------------------------------------------------------------------

current_mode() { cat "$MODE_FILE" 2>/dev/null || echo none; }
set_mode() { mkdir -p "$RUN_DIR"; echo "$1" >"$MODE_FILE"; }

# mode_dir MODE: where MODE keeps its data, binaries and environments.
mode_dir() {
  case "$1" in
    dev) printf '%s' "$RUN_DIR" ;;
    prod) printf '%s' "$RUN_DIR/prod" ;;
    *) die "unknown mode $1" ;;
  esac
}

# bin_dir MODE: compiled services and seed loaders. Mailpit and the
# collector are shared downloads in .run/bin.
bin_dir() { printf '%s/bin' "$(mode_dir "$1")"; }

# python_env MODE SERVICE: the virtualenv SERVICE runs from. Dev uses the
# service's own editable .venv (the one `just setup`, editors and `just test`
# use); prod gets a separate, non-editable environment without dev extras,
# as the Dockerfiles build.
python_env() {
  case "$1" in
    dev) printf '%s/services/%s/.venv' "$REPO_ROOT" "$2" ;;
    prod) printf '%s/venv/%s' "$(mode_dir prod)" "$2" ;;
  esac
}

# any_running: some native process recorded in .run/pids is alive.
any_running() {
  local name
  for name in "${SERVICES[@]}" "${SUPPORT_PROCESSES[@]}"; do
    is_running "$name" && return 0
  done
  pg_running dev || pg_running prod
}

# --- events backend -------------------------------------------------------------

# pubsub_emulator_installed: gcloud with the pubsub-emulator component, and
# Java to run it.
pubsub_emulator_installed() {
  command -v gcloud >/dev/null 2>&1 && command -v java >/dev/null 2>&1 || return 1
  local sdk_root
  sdk_root="$(gcloud info --format='value(installation.sdk_root)' 2>/dev/null)" || return 1
  [ -n "$sdk_root" ] && [ -d "$sdk_root/platform/pubsub-emulator" ]
}

# redis_events_supported: this checkout's platform/events has the Redis
# backend that services select with EVENTS_BACKEND=redis (phase3/local-first).
redis_events_supported() {
  grep -rqs --include='*.go' 'EVENTS_BACKEND' "$REPO_ROOT/platform/events"
}

# events_backend prints the backend every service uses: SKEIN_EVENTS_BACKEND
# if set, else pubsub when the emulator is installed, else redis when the
# services support it. It fails with instructions when neither works.
events_backend() {
  local chosen="${SKEIN_EVENTS_BACKEND:-}"
  if [ -z "$chosen" ]; then
    if pubsub_emulator_installed; then chosen=pubsub
    elif redis_events_supported; then chosen=redis
    else
      die "the Pub/Sub emulator is not installed and this checkout has no Redis events backend yet.
  Install the emulator: gcloud components install beta pubsub-emulator (and a Java 17+ runtime),
  or use the Docker stack: just dev docker.
  The Redis backend (EVENTS_BACKEND=redis) arrives with the local-first change (docs/local-first.md)."
    fi
  fi
  case "$chosen" in
    pubsub) pubsub_emulator_installed || die "SKEIN_EVENTS_BACKEND=pubsub, but the Pub/Sub emulator is not installed (just doctor)" ;;
    redis) redis_events_supported || die "SKEIN_EVENTS_BACKEND=redis, but this checkout's platform/events has no Redis backend yet" ;;
    memory) ;;
    *) die "SKEIN_EVENTS_BACKEND must be pubsub, redis or memory, not $chosen" ;;
  esac
  printf '%s' "$chosen"
}

# --- processes -------------------------------------------------------------------

# is_running NAME: the process recorded for NAME is alive.
is_running() {
  local pidfile="$PID_DIR/$1.pid"
  [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null
}

# start NAME CMD...: run CMD detached in a new session, stdout and stderr to
# .run/logs/NAME.log, and record its PID. The environment is the caller's,
# so callers pass service variables with `env -i`-style prefixes or exports
# in a subshell.
start() {
  local name="$1"; shift
  if is_running "$name"; then
    log "$name already running (pid $(cat "$PID_DIR/$name.pid"))"
    return 0
  fi
  mkdir -p "$PID_DIR" "$LOG_DIR"
  setsid "$@" >>"$LOG_DIR/$name.log" 2>&1 < /dev/null &
  echo $! >"$PID_DIR/$name.pid"
  log "started $name (pid $!), log $LOG_DIR/$name.log"
}

# stop NAME: SIGTERM the process group, wait up to 30 s (longer than every
# service's drain budget), then SIGKILL what is left.
stop() {
  local name="$1" pidfile="$PID_DIR/$1.pid" pid
  [ -f "$pidfile" ] || return 0
  pid="$(cat "$pidfile")"
  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
    for _ in $(seq 1 60); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.5
    done
    if kill -0 "$pid" 2>/dev/null; then
      log "$name did not stop in 30 s; killing"
      kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
    fi
    log "stopped $name"
  fi
  # The group may still hold children whose leader already exited.
  kill -KILL -- "-$pid" 2>/dev/null || true
  rm -f "$pidfile"
}

# wait_http NAME URL [SECONDS]: poll URL until it answers 2xx, failing early
# if NAME's process has died (its log tail is printed either way).
wait_http() {
  local name="$1" url="$2" timeout="${3:-90}"
  for _ in $(seq 1 $((timeout * 2))); do
    if curl -sf -o /dev/null --max-time 2 "$url"; then
      log "$name is ready"
      return 0
    fi
    if ! is_running "$name"; then
      tail -n 30 "$LOG_DIR/$name.log" >&2 || true
      die "$name exited during startup (log above)"
    fi
    sleep 0.5
  done
  tail -n 30 "$LOG_DIR/$name.log" >&2 || true
  die "$name not ready after ${timeout}s at $url (log above)"
}

# wait_port NAME PORT [SECONDS]: wait until something accepts TCP on PORT.
wait_port() {
  local name="$1" port="$2" timeout="${3:-60}"
  for _ in $(seq 1 $((timeout * 2))); do
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
      log "$name is accepting connections on $port"
      return 0
    fi
    if ! is_running "$name"; then
      tail -n 30 "$LOG_DIR/$name.log" >&2 || true
      die "$name exited during startup (log above)"
    fi
    sleep 0.5
  done
  die "$name not listening on $port after ${timeout}s"
}

# port_free PORT: nothing listens on PORT yet.
port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

# readyz_url SERVICE: the admin readiness probe of one of the seven services.
readyz_url() {
  local port
  case "$1" in
    identity) port=$SKEIN_NATIVE_IDENTITY_ADMIN ;;
    documents) port=$SKEIN_NATIVE_DOCUMENTS_ADMIN ;;
    search) port=$SKEIN_NATIVE_SEARCH_ADMIN ;;
    notifications) port=$SKEIN_NATIVE_NOTIFICATIONS_ADMIN ;;
    ml) port=$SKEIN_NATIVE_ML_ADMIN ;;
    agent) port=$SKEIN_NATIVE_AGENT_ADMIN ;;
    gateway) port=$SKEIN_NATIVE_GATEWAY_HTTP ;;
    *) die "unknown service $1" ;;
  esac
  printf 'http://127.0.0.1:%s/readyz' "$port"
}

# --- PostgreSQL --------------------------------------------------------------------

# as_postgres CMD...: PostgreSQL refuses to run as root, so when this script
# runs as root the server and its tools run as the postgres system user.
as_postgres() {
  if [ "$(id -u)" = 0 ]; then
    runuser -u postgres -- "$@"
  else
    "$@"
  fi
}

# pg_bin_dir prints the PostgreSQL 16 binary directory, or nothing; PG_BIN
# overrides the search.
pg_bin_dir() {
  local dir="${PG_BIN:-}" candidate
  if [ -z "$dir" ]; then
    for candidate in /usr/lib/postgresql/16/bin /usr/local/opt/postgresql@16/bin /opt/homebrew/opt/postgresql@16/bin; do
      [ -x "$candidate/initdb" ] && dir="$candidate" && break
    done
  fi
  [ -n "$dir" ] || dir="$(dirname "$(command -v initdb 2>/dev/null || echo /nonexistent/x)")"
  [ -x "$dir/initdb" ] && printf '%s' "$dir"
}

# pg_bin TOOL: path of a PostgreSQL 16 tool.
pg_bin() {
  local dir
  dir="$(pg_bin_dir)" || true
  [ -n "$dir" ] && [ -x "$dir/$1" ] || die "PostgreSQL 16 tool $1 not found; set PG_BIN (just doctor)"
  printf '%s/%s' "$dir" "$1"
}

# pg_running MODE: MODE's cluster is up.
pg_running() {
  local data
  data="$(mode_dir "$1")/pgdata"
  [ -f "$data/PG_VERSION" ] && [ -n "$(pg_bin_dir)" ] &&
    as_postgres "$(pg_bin pg_ctl)" -D "$data" status >/dev/null 2>&1
}

# stop_pg MODE: fast shutdown of MODE's cluster, if it runs.
stop_pg() {
  if pg_running "$1"; then
    as_postgres "$(pg_bin pg_ctl)" -D "$(mode_dir "$1")/pgdata" -m fast -w stop >/dev/null
    log "stopped postgres ($1)"
  fi
}

# --- pinned downloads ------------------------------------------------------------

# fetch_pinned NAME URL SHA256 MEMBER: download a release tarball, check it
# against the pinned SHA-256, and install MEMBER from it as .run/bin/NAME.
fetch_pinned() {
  local name="$1" url="$2" sum="$3" member="$4" tmp
  [ -x "$RUN_DIR/bin/$name" ] && return 0
  mkdir -p "$RUN_DIR/bin"
  tmp="$(mktemp -d)"
  log "downloading $name from $url"
  curl -sSfL -o "$tmp/archive.tgz" "$url" || { rm -rf "$tmp"; die "download of $name failed; put a $name binary in $RUN_DIR/bin"; }
  echo "$sum  $tmp/archive.tgz" | sha256sum -c --quiet - || { rm -rf "$tmp"; die "$name checksum mismatch"; }
  tar -xzf "$tmp/archive.tgz" -C "$tmp" "$member"
  install -m 0755 "$tmp/$member" "$RUN_DIR/bin/$name"
  rm -rf "$tmp"
}

# platform_arch prints amd64 or arm64 for Linux, or fails: the pinned
# downloads below are Linux builds.
platform_arch() {
  [ "$(uname -s)" = Linux ] || return 1
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) return 1 ;;
  esac
}

# Mailpit, the version compose pins.
MAILPIT_VERSION=v1.33.1
install_mailpit() {
  [ -x "$RUN_DIR/bin/mailpit" ] && return 0
  local arch sum
  arch="$(platform_arch)" || die "no pinned Mailpit build for $(uname -sm); put a mailpit binary in $RUN_DIR/bin"
  case "$arch" in
    amd64) sum=84d7fe375ebef25df26758984dc0d5d5e17d8c0734cb2e77be7873b14ab51716 ;;
    arm64) sum=3c5f076cb676d6980e47da030861c6d60f1f5614103dea93cccbcdfc43198e12 ;;
  esac
  fetch_pinned mailpit "https://github.com/axllent/mailpit/releases/download/$MAILPIT_VERSION/mailpit-linux-$arch.tar.gz" "$sum" mailpit
}

# The OpenTelemetry Collector core distribution (OTLP receiver, debug and
# Prometheus exporters are all prod-local needs). 0.157.0 is the newest
# release whose published checksum file could be verified when this was
# pinned; compose runs the contrib distribution for its larger pipeline.
OTELCOL_VERSION=0.157.0
install_otelcol() {
  [ -x "$RUN_DIR/bin/otelcol" ] && return 0
  local arch sum
  arch="$(platform_arch)" || die "no pinned collector build for $(uname -sm); put an otelcol binary in $RUN_DIR/bin"
  case "$arch" in
    amd64) sum=2937cf24892af55b143c072fddece17862239cf78280620029276493eb81beae ;;
    arm64) sum=59b63b99bab315509375fee76e22a9065eb9d9ba0a8995f8e985d60ca50d34ea ;;
  esac
  fetch_pinned otelcol "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v$OTELCOL_VERSION/otelcol_${OTELCOL_VERSION}_linux_$arch.tar.gz" "$sum" otelcol
}

# random_secret prints 32 random bytes as unpadded base64url: the format of
# identity's signing-key seeds, also used for generated local passwords.
random_secret() { head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n'; }
