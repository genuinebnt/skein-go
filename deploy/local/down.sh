#!/usr/bin/env bash
# Stops everything the native stack started (just stop, make down-native):
# the services first, in reverse dependency order so each drains while its
# dependencies are still up, then the web app, the collector and the
# infrastructure, including either mode's PostgreSQL cluster.
#
#   down.sh                  stop, keep the data
#   down.sh --wipe [MODE]    stop and delete MODE's data and the logs
#                            (MODE: dev, prod or all, the default);
#                            binaries and virtualenvs are kept
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

wipe=""
case "${1:-}" in
  "") ;;
  --wipe) wipe="${2:-all}" ;;
  *) die "usage: down.sh [--wipe [dev|prod|all]]" ;;
esac
case "$wipe" in ""|dev|prod|all) ;; *) die "--wipe takes dev, prod or all" ;; esac

for ((i = ${#SERVICES[@]} - 1; i >= 0; i--)); do
  stop "${SERVICES[$i]}"
done
for name in "${SUPPORT_PROCESSES[@]}"; do
  stop "$name"
done
stop_pg dev
stop_pg prod
rm -f "$BIN_OVERRIDES_FILE" "$RUN_DIR/events-backend"
[ "$(current_mode)" = docker ] || rm -f "$MODE_FILE"

wipe_mode() {
  local dir
  dir="$(mode_dir "$1")"
  rm -rf "$dir/pgdata" "$dir/pubsub"
  if [ "$1" = dev ]; then
    rm -f "$RUN_DIR/seed.stamp" "$RUN_DIR/seed-password"
  else
    rm -f "$dir/identity-signing-keys"
  fi
  log "wiped $1 data"
}
if [ -n "$wipe" ]; then
  case "$wipe" in
    all) wipe_mode dev; wipe_mode prod ;;
    *) wipe_mode "$wipe" ;;
  esac
  rm -rf "$LOG_DIR" "$RUN_DIR/logs.previous"
  log "wiped logs (binaries and virtualenvs kept)"
fi
