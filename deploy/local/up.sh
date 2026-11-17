#!/usr/bin/env bash
# Starts the whole Skein stack natively, without Docker: PostgreSQL 16,
# Redis, the events backend (the Pub/Sub emulator, or Redis when the
# emulator is not installed), Mailpit, and the seven services as plain
# processes on the ports in ports.env, waits for every readiness probe, and
# in dev loads a seed dataset.
#
#   up.sh [--mode dev|prod] [--seed small|medium|large|stress|none] [--web]
#
#   --mode dev   (default; just dev, make up-native) debug builds, dev
#                conveniences on, the small seed loaded
#   --mode prod  (just prod-local) release builds and production settings;
#                refuses to start if a dev flag is on (stack.sh lists the
#                differences and the stopgaps that stay)
#   --seed       the dataset to load in dev (default small)
#   --web        also start the web app in live mode against the gateway
#
# SKEIN_BIN_<SERVICE>=PATH runs PATH instead of that service's own build
# (for example a Rust port: SKEIN_BIN_SEARCH=../skein-rs/target/release/search),
# with the same environment, ports and readiness check; relative paths are
# relative to the repository root. docs/porting/README.md shows the loop.
#
# Running it again is safe: what is already running is left alone, and each
# dataset is loaded once. Starting a different mode than the one running
# stops the running one first (both use the same ports; their data is kept
# apart). State lives in deploy/local/.run (git-ignored).
#
# docs/dev-workflow.md and docs/local-native.md explain ports, logs and
# troubleshooting.
set -euo pipefail
MODE=dev
seed_profile=small
with_web=0
while [ $# -gt 0 ]; do
  case "$1" in
    --mode) MODE="${2:?--mode needs dev or prod}"; shift 2 ;;
    --seed) seed_profile="${2:?--seed needs a profile}"; shift 2 ;;
    --web) with_web=1; shift ;;
    *) echo "up.sh: unknown argument $1" >&2; exit 2 ;;
  esac
done
case "$MODE" in dev|prod) ;; *) echo "up.sh: --mode must be dev or prod" >&2; exit 2 ;; esac
export MODE

. "$(dirname "${BASH_SOURCE[0]}")/stack.sh"
use_pinned_tools
require_native_tools
mkdir -p "$RUN_DIR" "$PID_DIR" "$LOG_DIR"

running_mode="$(current_mode)"
if [ "$running_mode" = docker ]; then
  die "the Docker stack is recorded as running (just dev docker); stop it with just stop first"
fi

# Every refusal comes before anything is stopped or started.
EVENTS_BACKEND="$(events_backend)"
if [ "$MODE" = prod ]; then
  [ "$with_web" = 0 ] || die "--web is for dev; the web app's dev server is not a production setting"
  check_prod_settings
fi

if [ "$running_mode" != "$MODE" ] && [ "$running_mode" != none ] && any_running; then
  log "the $running_mode stack is running; stopping it before starting $MODE (its data is kept)"
  "$LOCAL_DIR/down.sh"
fi
# A fresh start gets fresh logs, so the prod-local checks (and anyone
# reading them) see only this run; the previous run's stay in logs.previous.
if ! any_running; then
  rm -rf "$RUN_DIR/logs.previous" "$BIN_OVERRIDES_FILE"
  if [ -d "$LOG_DIR" ]; then mv "$LOG_DIR" "$RUN_DIR/logs.previous"; fi
  mkdir -p "$LOG_DIR"
fi
set_mode "$MODE"
record_binary_overrides "${SERVICES[@]}"
echo "$EVENTS_BACKEND" >"$EVENTS_BACKEND_FILE"
[ "$EVENTS_BACKEND" = pubsub ] || log "events backend: $EVENTS_BACKEND (EVENTS_BACKEND=$EVENTS_BACKEND for every service)"

build_go "${GO_SERVICES[@]}"
build_python ml agent
start_infrastructure
for svc in "${SERVICES[@]}"; do
  start_service "$svc"
done
if [ "$MODE" = dev ] && [ "$seed_profile" != none ]; then
  load_seed "$(seed_dir "$seed_profile")" "$seed_profile"
fi
if [ "$with_web" = 1 ]; then start_web; fi
print_summary
