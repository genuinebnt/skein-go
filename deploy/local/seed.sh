#!/usr/bin/env bash
# Loads a seed dataset into the running dev stack (just seed [profile]):
#   seed.sh [small|medium|large|stress]     default small
# medium, large and stress are generated first (make seed) into
# bin/seed/<profile> when missing; docs/seed-data.md has their sizes and
# times. Each dataset is loaded once (SKEIN_NATIVE_RESEED=1 forces it).
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/stack.sh"
use_pinned_tools

profile="${1:-small}"
case "$(current_mode)" in
  dev) MODE=dev ;;
  prod) die "prod-local loads no seed data (no seed shortcuts in production settings); use just dev" ;;
  docker) exec make -C "$REPO_ROOT" --no-print-directory seed-load ;;
  *) die "no dev stack is running; just dev starts one (and loads the small seed)" ;;
esac
pg_running dev || die "the dev stack's PostgreSQL is not running; just dev"
mkdir -p "$LOG_DIR"
load_seed "$(seed_dir "$profile")" "$profile"
log "seed $profile loaded"
