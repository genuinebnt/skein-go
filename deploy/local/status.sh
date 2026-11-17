#!/usr/bin/env bash
# Prints what the native stack is running (just status): the mode, the
# events backend, every process with its PID and readiness, and the seed
# datasets loaded. Exits 1 when a recorded process is down or unready, so
# scripts can use it as a health check.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

mode="$(current_mode)"
if [ "$mode" = docker ]; then
  echo "mode: docker (compose); see docker compose ps, or make logs"
  exec docker compose -f "$REPO_ROOT/deploy/compose/docker-compose.yml" --profile '*' ps
fi
echo "mode:    $mode"
echo "events:  $(cat "$RUN_DIR/events-backend" 2>/dev/null || echo -)"

healthy=0
row() { printf '  %-14s %-9s %-8s %s\n' "$@"; }
row PROCESS STATE PID CHECK

# probe NAME URL: running and answering URL.
probe() {
  local name="$1" url="$2" pid=-
  [ -f "$PID_DIR/$name.pid" ] && pid="$(cat "$PID_DIR/$name.pid")"
  if ! is_running "$name"; then
    if [ "$pid" != - ]; then row "$name" down "$pid" "process exited (just logs $name)"; healthy=1; fi
    return 0
  fi
  if [ -z "$url" ] || curl -sf -o /dev/null --max-time 2 "$url"; then
    row "$name" ready "$pid" "${url:-tcp}"
  else
    row "$name" unready "$pid" "$url"
    healthy=1
  fi
}

for m in dev prod; do
  if pg_running "$m"; then row postgres ready - "127.0.0.1:$SKEIN_NATIVE_PG_PORT ($m data)"; fi
done
probe redis ""
probe pubsub "http://127.0.0.1:$SKEIN_NATIVE_PUBSUB_PORT"
probe mailpit "http://127.0.0.1:$SKEIN_NATIVE_MAILPIT_PORT/readyz"
probe otelcol "http://127.0.0.1:$SKEIN_NATIVE_OTELCOL_METRICS/metrics"
for svc in "${SERVICES[@]}"; do probe "$svc" "$(readyz_url "$svc")"; done
probe web "http://127.0.0.1:$SKEIN_NATIVE_WEB_PORT/"

if [ -s "$RUN_DIR/seed.stamp" ]; then
  echo "seed:    $(cut -d' ' -f1 "$RUN_DIR/seed.stamp" | paste -sd' ') (dev data)"
fi
if [ "$mode" = none ] && ! any_running; then
  echo "nothing is running (just dev starts the stack)"
fi
exit "$healthy"
