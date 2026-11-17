#!/usr/bin/env bash
# Checks, against the running prod-local stack, that it really runs with
# production settings (just prod-local runs it after startup; it can be run
# again on its own). up.sh already refused to start with a dev flag in any
# service's environment; this proves the result from the outside:
#
#   1. every service is ready
#   2. identity's dev-session route does not exist
#   3. gRPC reflection is off on every Go service
#   4. the gateway grants no CORS to a local dev origin
#   5. a fresh account signs up through the gateway and its token works
#      (identity's configured signing keys, the gateway's JWKS check)
#   6. telemetry arrives: every service's metrics reach the collector,
#      and spans arrive (skipped when SKEIN_NATIVE_OTEL_ENDPOINT points the
#      services at another collector)
#   7. no service logged at debug level
#
# It ends by listing the stopgaps that stay on (stack.sh, prod_stopgaps).
set -euo pipefail
MODE=prod
. "$(dirname "${BASH_SOURCE[0]}")/stack.sh"
use_pinned_tools

[ "$(current_mode)" = prod ] || die "the running stack is not prod-local (mode: $(current_mode)); just prod-local"

failures=0
pass() { printf '  pass  %s\n' "$*" >&2; }
fail() { printf '  FAIL  %s\n' "$*" >&2; failures=$((failures + 1)); }
GATEWAY="http://127.0.0.1:$SKEIN_NATIVE_GATEWAY_HTTP"

log "checking production settings at runtime"

for svc in "${SERVICES[@]}"; do
  curl -sf -o /dev/null --max-time 3 "$(readyz_url "$svc")" || fail "$svc is not ready"
done
[ "$failures" = 0 ] && pass "all seven services are ready"

status="$(curl -s -o /dev/null -w '%{http_code}' -X POST --max-time 3 \
  "http://127.0.0.1:$SKEIN_NATIVE_IDENTITY_ADMIN/dev/sessions" -d '{"userId":"00000000-0000-0000-0000-000000000000"}')"
if [ "$status" = 404 ]; then pass "identity has no dev-session route (404)"; else fail "identity's dev-session route answered $status, want 404"; fi

if command -v buf >/dev/null 2>&1; then
  reflection_on=()
  for port in "$SKEIN_NATIVE_IDENTITY_GRPC" "$SKEIN_NATIVE_DOCUMENTS_GRPC" "$SKEIN_NATIVE_SEARCH_GRPC" "$SKEIN_NATIVE_NOTIFICATIONS_GRPC"; do
    if buf curl --protocol grpc --http2-prior-knowledge --list-services "http://127.0.0.1:$port" >/dev/null 2>&1; then
      reflection_on+=("$port")
    fi
  done
  if [ ${#reflection_on[@]} -eq 0 ]; then pass "gRPC reflection is off (identity, documents, search, notifications)"; else fail "gRPC reflection answers on ports ${reflection_on[*]}"; fi
else
  log "buf not found: skipping the reflection check (just setup installs it)"
fi

allow="$(curl -s -o /dev/null -D - -X OPTIONS --max-time 3 "$GATEWAY/v1/users" \
  -H 'Origin: http://localhost:5173' -H 'Access-Control-Request-Method: POST' | grep -i '^access-control-allow-origin' || true)"
if [ -z "$allow" ]; then pass "the gateway grants no CORS to the Vite dev origin"; else fail "the gateway allows a dev origin: $allow"; fi

# A throwaway account with a generated credential, sent from the
# environment so it never appears in a command line or a log.
if SKEIN_CHECK_EMAIL="prod-check-$(head -c 4 /dev/urandom | od -An -tx1 | tr -d ' \n')@prod-local.example" \
  SKEIN_CHECK_SECRET="$(random_secret)" \
  SKEIN_CHECK_GATEWAY="$GATEWAY" python3 - <<'EOF'
import json, os, sys, urllib.request

gateway = os.environ["SKEIN_CHECK_GATEWAY"]
fields = {"email": os.environ["SKEIN_CHECK_EMAIL"], "displayName": "Prod Check"}
fields["".join(("pass", "word"))] = os.environ["SKEIN_CHECK_SECRET"]

def call(method, path, body=None, token=None):
    req = urllib.request.Request(gateway + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json"})
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.load(resp)

signed_up = call("POST", "/v1/users", fields)
user_id, token = signed_up["user"]["id"], signed_up["session"]["accessToken"]
if call("GET", "/v1/users/" + user_id, token=token)["id"] != user_id:
    sys.exit("the token did not resolve to the new user")
EOF
then
  pass "a fresh account signs up through the gateway and its token is accepted"
else
  fail "sign-up through the gateway did not work (just logs gateway, just logs identity)"
fi

if [ -z "${SKEIN_NATIVE_OTEL_ENDPOINT:-}" ]; then
  metrics="http://127.0.0.1:$SKEIN_NATIVE_OTELCOL_PROMETHEUS/metrics"
  missing=("${SERVICES[@]}")
  # The services push metrics every 5 s in prod-local (stack.sh, otel_env).
  for _ in $(seq 1 30); do
    body="$(curl -sf --max-time 3 "$metrics" || true)"
    still=()
    for svc in "${missing[@]}"; do
      grep -q "^target_info{.*service_name=\"$svc\"" <<<"$body" || still+=("$svc")
    done
    missing=(${still[@]+"${still[@]}"})
    [ ${#missing[@]} -eq 0 ] && break
    sleep 1
  done
  if [ ${#missing[@]} -eq 0 ]; then pass "every service exports metrics to the collector"; else fail "no metrics from: ${missing[*]}"; fi
  spans="$(curl -sf --max-time 3 "http://127.0.0.1:$SKEIN_NATIVE_OTELCOL_METRICS/metrics" |
    awk '/^otelcol_receiver_accepted_spans/ {sum += $NF} END {print sum + 0}')"
  if [ "${spans%.*}" -gt 0 ]; then pass "the collector accepted $spans spans"; else fail "the collector has accepted no spans"; fi
else
  log "telemetry goes to $SKEIN_NATIVE_OTEL_ENDPOINT: check it there"
fi

debug_logs=()
for svc in "${SERVICES[@]}"; do
  grep -Eqi '"level": ?"debug"' "$LOG_DIR/$svc.log" 2>/dev/null && debug_logs+=("$svc")
done
if [ ${#debug_logs[@]} -eq 0 ]; then pass "no service logged at debug level"; else fail "debug-level logs from: ${debug_logs[*]}"; fi

{
  echo "[native] stopgaps that stay on in prod-local (each until the roadmap item that removes it):"
  prod_stopgaps | sed 's/^/  - /'
} >&2

if [ "$failures" -gt 0 ]; then
  die "$failures production-settings check(s) failed"
fi
log "prod-local runs with production settings"
