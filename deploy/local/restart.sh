#!/usr/bin/env bash
# Rebuilds and restarts some services of a running native stack, leaving
# the rest (and the data) alone:
#   deploy/local/restart.sh documents gateway
# With no arguments it restarts all seven services.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
services=("$@")
[ ${#services[@]} -gt 0 ] || services=(gateway agent ml notifications search documents identity)
for name in "${services[@]}"; do
  case "$name" in
    identity|documents|search|notifications|ml|agent|gateway) stop "$name" ;;
    *) die "unknown service $name" ;;
  esac
done
exec "$LOCAL_DIR/up.sh"
