#!/usr/bin/env bash
# Stops everything make up-native started, services first (in reverse
# dependency order, so each drains while its dependencies are still up),
# then infrastructure. Data stays in deploy/local/.run; pass --wipe (make
# down-native WIPE=1) to delete it as well.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

for name in gateway agent ml notifications search documents identity mailpit pubsub redis; do
  stop "$name"
done

if [ -f "$RUN_DIR/pgdata/PG_VERSION" ] &&
  as_postgres "$(pg_bin pg_ctl)" -D "$RUN_DIR/pgdata" status >/dev/null 2>&1; then
  as_postgres "$(pg_bin pg_ctl)" -D "$RUN_DIR/pgdata" -m fast -w stop >/dev/null
  log "stopped postgres"
fi

if [ "${1:-}" = "--wipe" ]; then
  rm -rf "$RUN_DIR/pgdata" "$RUN_DIR/pubsub" "$RUN_DIR/logs" "$RUN_DIR/seed.stamp"
  log "wiped data and logs (binaries and virtualenvs kept)"
fi
