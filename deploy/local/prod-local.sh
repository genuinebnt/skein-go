#!/usr/bin/env bash
# Runs the native stack with production settings and proves it (just
# prod-local): up.sh --mode prod (release builds, production configuration,
# telemetry on, no seed; refuses to start with a dev flag on), then
# prod-check.sh against the running stack. A running dev stack is stopped
# first; its data is kept. Stop with just stop.
set -euo pipefail
here="$(dirname "${BASH_SOURCE[0]}")"
"$here/up.sh" --mode prod
exec "$here/prod-check.sh"
