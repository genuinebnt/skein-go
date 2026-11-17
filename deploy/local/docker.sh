#!/usr/bin/env bash
# The Docker variant of the stack (just dev docker, just stop): the compose
# stack from the Makefile, recorded in .run/mode so just status, logs and
# stop know which stack is up. Needs a Docker daemon; the native stack is
# the default and needs none.
#   docker.sh up     make up-app (infrastructure and the seven services), then the small seed
#   docker.sh down   make down (data kept in the compose volumes)
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

case "${1:-}" in
  up)
    docker info >/dev/null 2>&1 || die "no Docker daemon answers; start Docker, or use the native stack (just dev)"
    if [ "$(current_mode)" != none ] && [ "$(current_mode)" != docker ] && any_running; then
      die "the native $(current_mode) stack is running on the same machine; just stop first"
    fi
    make -C "$REPO_ROOT" --no-print-directory up-app
    make -C "$REPO_ROOT" --no-print-directory seed-load
    set_mode docker
    make -C "$REPO_ROOT" --no-print-directory urls
    ;;
  down)
    make -C "$REPO_ROOT" --no-print-directory down
    rm -f "$MODE_FILE"
    ;;
  *) die "usage: docker.sh up|down" ;;
esac
