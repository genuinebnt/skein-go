#!/usr/bin/env bash
# Runs the web app's dev server in the foreground (just web, just web-live):
#   web.sh mock   no backend: the mock API and the seed built into the app,
#                 on WEB_PORT (default 5173); the same as make web-dev
#   web.sh live   against the running native gateway: /v1 is proxied to it
#                 (web/vite.config.ts), on SKEIN_NATIVE_WEB_PORT (25173)
# `just dev web` starts live mode in the background with the stack instead.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
use_pinned_tools

mode="${1:-mock}"
case "$mode" in
  mock) exec make -C "$REPO_ROOT" --no-print-directory web-dev WEB_PORT="${WEB_PORT:-5173}" ;;
  live) ;;
  *) die "usage: web.sh mock|live" ;;
esac

gateway="http://127.0.0.1:$SKEIN_NATIVE_GATEWAY_HTTP"
curl -sf -o /dev/null --max-time 2 "$gateway/readyz" || die "no gateway answers at $gateway; start the stack with just dev"
is_running web && die "the web app already runs in the background (just dev web); open http://127.0.0.1:$SKEIN_NATIVE_WEB_PORT"
cd "$REPO_ROOT/web"
pnpm install --frozen-lockfile
sh wasm/build.sh --if-missing
log "web app (live) on http://127.0.0.1:$SKEIN_NATIVE_WEB_PORT, /v1 proxied to $gateway"
VITE_GATEWAY="$gateway" exec pnpm exec vite --mode live --host 127.0.0.1 --port "$SKEIN_NATIVE_WEB_PORT" --strictPort
