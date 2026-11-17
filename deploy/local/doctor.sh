#!/usr/bin/env bash
# Checks this machine for everything the Skein workflow needs (just doctor)
# and prints, for each missing or mismatched piece, how to get it.
#
#   ok    present, at the pinned version where one is pinned
#   warn  works, but something is off (another version, an optional piece
#         missing); the line says what it affects
#   FAIL  `just dev` (native) cannot work until it is fixed
#
# Pinned versions come from mise.toml, so this file never repeats them.
# Exits 1 if any line is FAIL.
set -uo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
use_pinned_tools

fails=0 warns=0
ok() { printf '  ok    %-26s %s\n' "$1" "$2"; }
warn() { printf '  warn  %-26s %s\n        -> %s\n' "$1" "$2" "$3"; warns=$((warns + 1)); }
fail() { printf '  FAIL  %-26s %s\n        -> %s\n' "$1" "$2" "$3"; fails=$((fails + 1)); }
section() { printf '\n%s\n' "$1"; }
have() { command -v "$1" >/dev/null 2>&1; }

# pin TOOL: the version mise.toml pins for TOOL (a [tools] key).
pin() {
  python3 -c '
import sys, tomllib
with open(sys.argv[1], "rb") as f:
    print(tomllib.load(f).get("tools", {}).get(sys.argv[2], ""))
' "$REPO_ROOT/mise.toml" "$1" 2>/dev/null
}

# check_pinned LABEL MISE_KEY COMMAND VERSION_CMD: COMMAND is on PATH and
# VERSION_CMD's output contains the pinned version.
check_pinned() {
  local label="$1" key="$2" cmd="$3" version_cmd="$4" want got
  want="$(pin "$key")"
  if ! have "$cmd"; then
    fail "$label" "not found (pinned $want)" "just setup (mise install), or install $label $want yourself"
    return
  fi
  got="$(eval "$version_cmd" 2>&1 | head -n 1)"
  if [ -n "$want" ] && [[ "$got" != *"$want"* ]]; then
    warn "$label" "$got (pinned $want)" "just setup installs the pinned version; with mise, activate it in your shell (mise activate)"
  else
    ok "$label" "$got"
  fi
}

echo "Skein doctor: $REPO_ROOT"

section "Version manager"
if have mise; then
  if (cd "$REPO_ROOT" && mise env -s bash >/dev/null 2>&1); then
    missing="$(cd "$REPO_ROOT" && mise ls --current --missing 2>/dev/null | awk '{print $1"@"$2}' | paste -sd' ')"
    if [ -n "$missing" ]; then
      warn mise "$(mise --version | cut -d' ' -f1); not installed yet: $missing" "just setup (runs mise install)"
    else
      ok mise "$(mise --version | cut -d' ' -f1), every tool in mise.toml installed"
    fi
  else
    warn mise "installed, but mise.toml is not trusted" "mise trust (or just setup)"
  fi
else
  warn mise "not found; using whatever is on PATH" "curl https://mise.run | sh  (https://mise.jdx.dev), then just setup"
fi

section "Pinned tools (mise.toml)"
check_pinned go go go "go env GOVERSION"
check_pinned python python python3 "python3 --version"
check_pinned node node node "node --version"
check_pinned pnpm pnpm pnpm "pnpm --version"
check_pinned uv uv uv "uv --version"
check_pinned just just just "just --version"
check_pinned buf buf buf "buf --version"
check_pinned protoc-gen-go "go:google.golang.org/protobuf/cmd/protoc-gen-go" protoc-gen-go "protoc-gen-go --version"
check_pinned protoc-gen-go-grpc "go:google.golang.org/grpc/cmd/protoc-gen-go-grpc" protoc-gen-go-grpc "protoc-gen-go-grpc --version"
check_pinned protoc-gen-grpc-gateway "go:github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway" protoc-gen-grpc-gateway "protoc-gen-grpc-gateway --version"
check_pinned protoc-gen-openapiv2 "go:github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2" protoc-gen-openapiv2 "protoc-gen-openapiv2 --version"

section "System tools for the native stack (not managed by mise)"
pg_dir="$(pg_bin_dir || true)"
if [ -n "$pg_dir" ]; then
  pg_version="$("$pg_dir/initdb" --version)"
  case "$pg_version" in
    *" 16."*) ok "PostgreSQL server" "$pg_version ($pg_dir)" ;;
    *) warn "PostgreSQL server" "$pg_version, want 16" "install PostgreSQL 16 server binaries and set PG_BIN to their bin directory" ;;
  esac
else
  fail "PostgreSQL server" "initdb not found" "apt install postgresql-16 (Debian/Ubuntu) or brew install postgresql@16; set PG_BIN if it lives elsewhere"
fi
if have redis-server; then
  redis_version="$(redis-server --version | grep -o 'v=[0-9.]*' | cut -c3-)"
  case "$redis_version" in
    [7-9].*) ok redis-server "$redis_version" ;;
    *) warn redis-server "$redis_version, want 7 or later" "apt install redis-server or brew install redis" ;;
  esac
else
  fail redis-server "not found" "apt install redis-server or brew install redis"
fi
for tool in curl git make setsid sha256sum tar; do
  if have "$tool"; then ok "$tool" "$(command -v "$tool")"; else fail "$tool" "not found" "install $tool with your package manager (setsid is in util-linux; sha256sum in coreutils)"; fi
done
if [ "$(id -u)" = 0 ]; then
  if have runuser && id postgres >/dev/null 2>&1; then
    ok "postgres user" "running as root: PostgreSQL runs as the postgres user"
  else
    fail "postgres user" "running as root without runuser or a postgres user" "run as a normal user, or create the postgres system user (the Debian package does)"
  fi
fi

section "Events backend (the Pub/Sub emulator: gcloud, its component, Java; or Redis)"
redis_missing="$(redis_events_missing)"
if [ -z "$redis_missing" ]; then
  ok "Redis events backend" "every service supports EVENTS_BACKEND=redis"
else
  ok "Redis events backend" "not in this checkout yet (missing in: $redis_missing); the emulator is needed"
fi
if pubsub_emulator_installed; then
  ok "Pub/Sub emulator" "$(gcloud --version 2>/dev/null | head -n 1); $(java -version 2>&1 | grep -m1 version)"
else
  reason="missing:"
  have gcloud || reason="$reason gcloud"
  have java || reason="$reason java"
  if have gcloud && have java; then reason="$reason the pubsub-emulator component"; fi
  how="gcloud components install beta pubsub-emulator (Google Cloud CLI: https://cloud.google.com/sdk/docs/install) and a Java 17+ runtime"
  if [ -z "$redis_missing" ]; then
    warn "Pub/Sub emulator" "$reason; just dev uses EVENTS_BACKEND=redis instead" "$how"
  else
    fail "Pub/Sub emulator" "$reason, and the Redis events backend is not in this checkout yet" "$how; or use the Docker stack (just dev docker)"
  fi
fi

section "Project environments (just setup builds them)"
for svc in ml agent; do
  venv="$REPO_ROOT/services/$svc/.venv"
  if [ -x "$venv/bin/python" ]; then ok "$svc venv" "$venv"; else warn "$svc venv" "missing" "just setup"; fi
done
if [ -x "$REPO_ROOT/.venv/bin/python" ] && "$REPO_ROOT/.venv/bin/python" -c 'import grpc_tools' 2>/dev/null; then
  ok "tools venv" "$REPO_ROOT/.venv (grpcio-tools for just proto)"
else
  warn "tools venv" "missing or without grpcio-tools; just proto cannot generate Python" "just setup"
fi
if [ -d "$REPO_ROOT/web/node_modules" ]; then ok "web dependencies" "web/node_modules"; else warn "web dependencies" "missing" "just setup (pnpm install)"; fi
if ls "${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}"/chromium* >/dev/null 2>&1; then
  ok "Playwright browsers" "installed"
else
  warn "Playwright browsers" "not installed; only just web-e2e needs them" "cd web && pnpm exec playwright install chromium"
fi

section "Optional: Docker (only for just dev docker)"
if ! have docker; then
  warn docker "not installed; the native stack does not need it" "https://docs.docker.com/engine/install/"
elif docker info >/dev/null 2>&1; then
  ok docker "$(docker --version)"
else
  warn docker "CLI found but no daemon answers; just dev docker will not work" "start Docker (or use the native stack, the default)"
fi

section "Ports (deploy/local/ports.env; override any with its variable)"
taken=()
for var in $(grep -o 'SKEIN_NATIVE_[A-Z_]*:=' "$LOCAL_DIR/ports.env" | tr -d ':='); do
  port="${!var}"
  port_free "$port" || taken+=("$var=$port")
done
if [ ${#taken[@]} -eq 0 ]; then
  ok "ports" "all free"
elif [ "$(current_mode)" != none ] && any_running; then
  ok "ports" "${#taken[@]} in use by the running native stack ($(current_mode))"
else
  warn "ports" "in use: ${taken[*]}" "stop what listens there, or override the variable (SKEIN_NATIVE_PG_PORT=35432 just dev)"
fi

printf '\n'
if [ "$fails" -gt 0 ]; then
  echo "$fails problem(s) block the native stack and $warns warning(s); fix the FAIL lines, then run just doctor again."
  exit 1
fi
echo "Ready: no blocking problems ($warns warning(s))."
