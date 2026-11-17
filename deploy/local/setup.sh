#!/usr/bin/env bash
# One-time (and safe to repeat) setup of a fresh clone (just setup):
#   1. mise trust + mise install: the tools pinned in mise.toml
#   2. Go modules for every module in go.work
#   3. pnpm install for the web app
#   4. a virtualenv per Python service (services/<name>/.venv, editable,
#      with dev extras: pytest, ruff), and the repository's tools venv
#      (.venv: grpcio-tools at the version scripts/gen-python.sh pins, and
#      the generated Python package with its tests)
#   5. just doctor, for what mise does not manage (PostgreSQL, Redis, the
#      Pub/Sub emulator, Docker)
# Without mise it uses the tools already on PATH and says so.
set -euo pipefail
MODE=dev
. "$(dirname "${BASH_SOURCE[0]}")/stack.sh"

step() { printf '\n[setup] %s\n' "$*" >&2; }

# shellcheck disable=SC1091
[ -f "$HOME/.skein-env" ] && . "$HOME/.skein-env"
step "tools (mise.toml)"
if command -v mise >/dev/null 2>&1; then
  (cd "$REPO_ROOT" && mise trust --quiet "$REPO_ROOT/mise.toml" && mise install)
else
  log "mise is not installed: using the tools on PATH. Get it with: curl https://mise.run | sh (https://mise.jdx.dev)"
fi
use_pinned_tools
require go "install Go (mise install, or https://go.dev/dl)"
require uv "install uv (mise install, or https://docs.astral.sh/uv)"
require pnpm "install pnpm (mise install, or corepack enable)"
mkdir -p "$LOG_DIR"

step "Go modules (go.work)"
(cd "$REPO_ROOT" && go mod download)

step "web dependencies (pnpm install)"
(cd "$REPO_ROOT/web" && pnpm install --frozen-lockfile)

step "Python environments (services/ml/.venv, services/agent/.venv)"
build_python ml agent
log "ml and agent environments are current"

step "tools environment (.venv: grpcio-tools, gen/python tests)"
grpcio_tools="$(sed -n 's/^GRPCIO_TOOLS_VERSION=//p' "$REPO_ROOT/scripts/gen-python.sh")"
[ -n "$grpcio_tools" ] || die "could not read GRPCIO_TOOLS_VERSION from scripts/gen-python.sh"
(
  cd "$REPO_ROOT"
  uv venv -q --allow-existing --python "$(command -v python3)" .venv
  uv pip install -q --python .venv/bin/python "grpcio-tools==$grpcio_tools" -e "gen/python[test]"
)
log "tools environment ready (grpcio-tools $grpcio_tools)"

step "doctor"
exec "$LOCAL_DIR/doctor.sh"
