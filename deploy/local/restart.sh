#!/usr/bin/env bash
# Rebuilds and restarts some services of a running native stack, leaving
# every other process (and the data) alone (just restart <service>):
#   deploy/local/restart.sh documents gateway
# With no arguments it restarts all seven services. The running stack's mode
# (dev or prod) decides how they are built and configured.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/stack.sh"
use_pinned_tools

MODE="$(current_mode)"
case "$MODE" in
  dev|prod) ;;
  docker) die "the Docker stack is running; restart a service there with docker compose (make logs, make up-app)" ;;
  *) die "no native stack is running; start one with just dev" ;;
esac
[ -n "$EVENTS_BACKEND" ] || die "the running stack recorded no events backend; restart it with just stop && just dev"
[ "$MODE" = prod ] && check_prod_settings

services=("$@")
[ ${#services[@]} -gt 0 ] || services=("${SERVICES[@]}")
go_targets=() python_targets=()
for name in "${services[@]}"; do
  case "$name" in
    identity|documents|search|notifications|gateway) go_targets+=("$name") ;;
    ml|agent) python_targets+=("$name") ;;
    *) die "unknown service $name (one of: ${SERVICES[*]})" ;;
  esac
done

# Build first, so a compile error leaves the old process serving.
[ ${#go_targets[@]} -eq 0 ] || build_go "${go_targets[@]}"
[ ${#python_targets[@]} -eq 0 ] || build_python "${python_targets[@]}"

# Restart in dependency order whatever order they were named in.
for svc in "${SERVICES[@]}"; do
  for name in "${services[@]}"; do
    [ "$svc" = "$name" ] || continue
    stop "$svc"
    start_service "$svc"
  done
done

if [ "$MODE" = dev ] && printf '%s\n' "${services[@]}" | grep -qx identity; then
  log "identity generates its signing key at startup in dev: existing sessions are now invalid; sign in again"
fi
