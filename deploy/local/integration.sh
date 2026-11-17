#!/usr/bin/env bash
# Runs every integration test (Go: build tag integration; Python: pytest
# marker integration) against the infrastructure of a running native stack
# (just dev, make up-native) instead of testcontainers, and prints pass,
# fail and skip counts per module (just test-integration, make
# integration-native).
#
# Isolation: each Go test gets its own fresh database
# (platform/postgres/pgtest), Redis keys get a per-run prefix, Pub/Sub
# topics a per-run project, and each Python suite a fresh database. Nothing
# the running services own is touched, so the stack stays usable.
#
# Only the tests defined in integration-tagged files run (by name), so the
# counts are integration tests, not the unit tests beside them.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
use_pinned_tools

PG_ADMIN="postgres://skein@127.0.0.1:$SKEIN_NATIVE_PG_PORT/postgres?sslmode=disable"
export SKEIN_TEST_POSTGRES_DSN="$PG_ADMIN"
export SKEIN_TEST_REDIS_URL="redis://127.0.0.1:$SKEIN_NATIVE_REDIS_PORT/15"
export SKEIN_TEST_PUBSUB_EMULATOR_HOST="127.0.0.1:$SKEIN_NATIVE_PUBSUB_PORT"
export OTEL_SDK_DISABLED=true

port_free "$SKEIN_NATIVE_PG_PORT" && die "PostgreSQL is not running on $SKEIN_NATIVE_PG_PORT; start the stack with just dev"

REPORT="$RUN_DIR/integration-report.txt"
: >"$REPORT"
status=0

# count_go reads go test -json on stdin and prints "pass fail skip" for the
# named tests (top-level and their subtests).
count_go() {
  python3 -c '
import json, sys
names = set(sys.argv[1].split("|"))
counts = {"pass": 0, "fail": 0, "skip": 0}
for line in sys.stdin:
    try:
        e = json.loads(line)
    except ValueError:
        continue
    test = e.get("Test")
    if test and test.split("/")[0] in names and e.get("Action") in counts:
        counts[e["Action"]] += 1
    if e.get("Action") == "output" and ("--- FAIL" in e.get("Output", "") or "panic:" in e.get("Output", "")):
        sys.stderr.write(e["Output"])
print(counts["pass"], counts["fail"], counts["skip"])
' "$1"
}

run_go_module() {
  local module="$1" files names pkgs line
  files=$(cd "$REPO_ROOT/$module" && grep -rl --include='*_test.go' '^//go:build integration' . || true)
  [ -n "$files" ] || return 0
  # $files and $pkgs are space-separated lists on purpose.
  # shellcheck disable=SC2086
  names=$(cd "$REPO_ROOT/$module" && grep -ho '^func \(Test[A-Za-z0-9_]*\)' $files | sed 's/^func //' | sort -u | paste -sd'|')
  pkgs=$(for f in $files; do dirname "$f"; done | sort -u | tr '\n' ' ')
  log "integration: $module ($pkgs)"
  # shellcheck disable=SC2086
  line=$(cd "$REPO_ROOT/$module" && go test -tags integration -count=1 -timeout 20m -json -run "^($names)\$" $pkgs 2>"$LOG_DIR/integration-${module//\//-}.err" |
    tee "$LOG_DIR/integration-${module//\//-}.json" | count_go "$names") || true
  read -r pass fail skip <<<"$line"
  printf '%-24s pass %3d  fail %3d  skip %3d\n' "$module" "$pass" "$fail" "$skip" | tee -a "$REPORT"
  if [ "$fail" != 0 ] || grep -q '"Action":"fail","Package"' "$LOG_DIR/integration-${module//\//-}.json"; then
    status=1
    grep -h '"Output"' "$LOG_DIR/integration-${module//\//-}.json" | python3 -c '
import json, sys
for l in sys.stdin:
    o = json.loads(l).get("Output", "")
    if "FAIL" in o or "_test.go:" in o or "panic" in o:
        sys.stderr.write(o)' | tail -n 40 >&2 || true
  fi
}

# fresh_db NAME: drop and create a database for a Python suite.
fresh_db() {
  PGOPTIONS="-c client_min_messages=warning" "$(pg_bin psql)" "$PG_ADMIN" -q -c "DROP DATABASE IF EXISTS $1 WITH (FORCE)" -c "CREATE DATABASE $1" >/dev/null
}
drop_db() { PGOPTIONS="-c client_min_messages=warning" "$(pg_bin psql)" "$PG_ADMIN" -q -c "DROP DATABASE IF EXISTS $1 WITH (FORCE)" >/dev/null; }

run_pytest() {
  local service="$1" var="$2" db="skein_it_$1" xml="$RUN_DIR/integration-$1.xml"
  fresh_db "$db"
  log "integration: services/$service (pytest -m integration)"
  (cd "$REPO_ROOT/services/$service" &&
    env "$var=postgresql://skein@127.0.0.1:$SKEIN_NATIVE_PG_PORT/$db" \
      "$(python_env dev "$service")/bin/python" -m pytest -q -m integration -p no:cacheprovider --junitxml="$xml" \
      >"$LOG_DIR/integration-$service.log" 2>&1) || status=1
  drop_db "$db"
  python3 -c '
import sys, xml.etree.ElementTree as ET
r = ET.parse(sys.argv[1]).getroot()
s = r if r.tag == "testsuite" else r.find("testsuite")
total, fail, err, skip = (int(s.get(k, 0)) for k in ("tests", "failures", "errors", "skipped"))
print("%-24s pass %3d  fail %3d  skip %3d" % (sys.argv[2], total - fail - err - skip, fail + err, skip))
' "$xml" "services/$service" | tee -a "$REPORT"
  grep -E "FAILED|Error" "$LOG_DIR/integration-$service.log" | head -n 20 >&2 || true
}

for module in platform services/identity services/documents services/search services/notifications services/gateway; do
  run_go_module "$module"
done
run_pytest ml SKEIN_ML_TEST_DATABASE_URL
run_pytest agent SKEIN_AGENT_TEST_DATABASE_URL

log "summary (also in $REPORT)"
cat "$REPORT" >&2
exit "$status"
