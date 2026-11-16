#!/bin/sh
# Static checks for the local stack's configuration; `make stack-check` runs
# it. Needs no running stack. Checks:
#   - docker compose config --quiet for each profile (Docker CLI only, no daemon)
#   - every dashboard is valid JSON with a uid, a title and targets with an
#     expression, and every data source it names (panels, targets,
#     variables) is a provisioned one
#   - the collector config, alone and with the GKE overlay (otelcol validate)
#   - the Jaeger config (jaeger validate)
#   - the Prometheus config (promtool check config)
#   - the Loki config (loki -verify-config)
#
# Validators run from the same pinned images as the stack, so they check
# against the versions that will run. Without a Docker daemon, point
# OTELCOL, JAEGER, PROMTOOL and LOKI at local binaries of the same versions;
# a validator with neither is reported as SKIP, not as a pass.
set -eu

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OBS="$ROOT/deploy/observability"
COMPOSE="docker compose -f $ROOT/deploy/compose/docker-compose.yml"
failures=0
skips=0

pass() { echo "ok   $1"; }
fail() { echo "FAIL $1"; failures=$((failures + 1)); }
skip() { echo "SKIP $1"; skips=$((skips + 1)); }

image_of() { # service -> pinned image from the compose file
  $COMPOSE --profile '*' config --format json | jq -r ".services[\"$1\"].image"
}

# run_tool ENV_VAR service entrypoint args...: a local binary named by ENV_VAR,
# else the service's image (config files are mounted at the same path).
run_tool() {
  var=$1 service=$2 entrypoint=$3
  shift 3
  binary=$(eval "printf '%s' \"\${$var:-}\"")
  if [ -n "$binary" ]; then
    "$binary" "$@"
  elif docker info >/dev/null 2>&1; then
    docker run --rm -e DEPLOYMENT_ENVIRONMENT=check -v "$OBS:$OBS:ro" \
      --entrypoint "$entrypoint" "$(image_of "$service")" "$@"
  else
    return 125
  fi
}

check_tool() { # description ENV_VAR service entrypoint args...
  description=$1
  shift
  set +e
  output=$(run_tool "$@" 2>&1)
  status=$?
  set -e
  case $status in
    0) pass "$description" ;;
    125) skip "$description (no Docker daemon; set $1 to a local binary)" ;;
    *) fail "$description"; echo "$output" | tail -20 ;;
  esac
}

for profile in "" observability '*'; do
  label=${profile:-none}
  if [ -n "$profile" ]; then set -- --profile "$profile"; else set --; fi
  if $COMPOSE "$@" config --quiet; then pass "compose config, profile $label"; else fail "compose config, profile $label"; fi
done

for dashboard in "$OBS"/grafana/dashboards/*.json; do
  name=$(basename "$dashboard")
  if jq -e '
      (.uid | type == "string") and (.title | type == "string")
      and ([.panels[].targets[]?] | length > 0)
      and all(.. | objects | .datasource? | objects; .uid == "prometheus" or .uid == "loki")
      and all(.panels[].targets[]?; (.expr | type == "string") and (.expr | length > 0))
    ' "$dashboard" >/dev/null; then
    pass "dashboard $name"
  else
    fail "dashboard $name (invalid JSON, missing uid/title, empty target, or unknown data source)"
  fi
done
duplicates=$(jq -r .uid "$OBS"/grafana/dashboards/*.json | sort | uniq -d)
if [ -z "$duplicates" ]; then pass "dashboard uids are unique"; else fail "duplicate dashboard uids: $duplicates"; fi

export DEPLOYMENT_ENVIRONMENT=check
check_tool "collector config" OTELCOL otel-collector /otelcol-contrib \
  validate --config="$OBS/otel-collector.yaml"
check_tool "collector config with GKE overlay" OTELCOL otel-collector /otelcol-contrib \
  validate --config="$OBS/otel-collector.yaml" --config="$OBS/otel-collector.gke.yaml"
check_tool "jaeger config" JAEGER jaeger /cmd/jaeger/jaeger-linux \
  validate --config="$OBS/jaeger.yaml"
check_tool "prometheus config" PROMTOOL prometheus /bin/promtool \
  check config "$OBS/prometheus.yaml"
check_tool "loki config" LOKI loki /usr/bin/loki \
  -config.file="$OBS/loki.yaml" -verify-config

echo "$failures failure(s), $skips skipped"
[ "$failures" -eq 0 ]
