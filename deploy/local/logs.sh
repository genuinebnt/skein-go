#!/usr/bin/env bash
# Follows the native stack's logs (just logs [name]): one process's log, or
# all of them interleaved with a header per file. Logs are JSON, one file
# per process, in deploy/local/.run/logs.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

if [ "$(current_mode)" = docker ]; then
  exec docker compose -f "$REPO_ROOT/deploy/compose/docker-compose.yml" --profile '*' logs -f ${1:+"$1"}
fi
[ -d "$LOG_DIR" ] || die "no logs yet ($LOG_DIR); start the stack with just dev"
if [ $# -gt 0 ]; then
  if [ ! -f "$LOG_DIR/$1.log" ]; then
    have=()
    for file in "$LOG_DIR"/*.log; do have+=("$(basename "$file" .log)"); done
    die "no log for $1; have: ${have[*]}"
  fi
  exec tail -n "${LINES_BACK:-100}" -F "$LOG_DIR/$1.log"
fi
names=()
for name in "${SERVICES[@]}" "${SUPPORT_PROCESSES[@]}" postgres; do
  [ -f "$LOG_DIR/$name.log" ] && names+=("$LOG_DIR/$name.log")
done
[ ${#names[@]} -gt 0 ] || die "no process logs in $LOG_DIR"
exec tail -n "${LINES_BACK:-20}" -F "${names[@]}"
