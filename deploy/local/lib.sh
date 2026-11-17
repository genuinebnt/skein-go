# Helpers shared by up.sh and down.sh. Sourced, not executed.
#
# Every long-running process is started in its own session (setsid), so its
# process group ID equals the PID written to .run/pids/<name>.pid. Stopping
# signals the whole group: that matters for the Pub/Sub emulator, where
# gcloud starts a Java child that would otherwise outlive it.

LOCAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$LOCAL_DIR/../.." && pwd)"
RUN_DIR="$LOCAL_DIR/.run"
PID_DIR="$RUN_DIR/pids"
LOG_DIR="$RUN_DIR/logs"
BIN_DIR="$RUN_DIR/bin"
VENV_DIR="$RUN_DIR/venv"

# shellcheck source=ports.env
. "$LOCAL_DIR/ports.env"

log() { printf '[native] %s\n' "$*" >&2; }
die() { printf '[native] error: %s\n' "$*" >&2; exit 1; }

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
  local name="$1" pidfile="$PID_DIR/$1.pid" pid i
  [ -f "$pidfile" ] || return 0
  pid="$(cat "$pidfile")"
  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
    for i in $(seq 1 60); do
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
  local name="$1" url="$2" timeout="${3:-90}" i
  for i in $(seq 1 $((timeout * 2))); do
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
  local name="$1" port="$2" timeout="${3:-60}" i
  for i in $(seq 1 $((timeout * 2))); do
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

# as_postgres CMD...: PostgreSQL refuses to run as root, so when this script
# runs as root the server and its tools run as the postgres system user.
as_postgres() {
  if [ "$(id -u)" = 0 ]; then
    runuser -u postgres -- "$@"
  else
    "$@"
  fi
}

# pg_bin TOOL: path of a PostgreSQL 16 tool; PG_BIN overrides the directory.
pg_bin() {
  local dir="${PG_BIN:-}"
  if [ -z "$dir" ]; then
    for candidate in /usr/lib/postgresql/16/bin /usr/local/opt/postgresql@16/bin /opt/homebrew/opt/postgresql@16/bin; do
      [ -x "$candidate/$1" ] && dir="$candidate" && break
    done
  fi
  [ -n "$dir" ] || dir="$(dirname "$(command -v "$1" 2>/dev/null || echo /nonexistent/x)")"
  [ -x "$dir/$1" ] || die "PostgreSQL 16 tool $1 not found; set PG_BIN"
  printf '%s/%s' "$dir" "$1"
}
