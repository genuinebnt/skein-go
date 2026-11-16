#!/usr/bin/env bash
# Smoke test for a running stack (make up, with the small seed loaded):
# signs in as a seed user through the gateway and calls a few routes,
# checking the conventions of docs/api/README.md on the way (request IDs,
# rate-limit headers, google.rpc.Status errors, idempotent replay).
#
#   SMOKE_PASSWORD=... services/gateway/scripts/smoke.sh
#
# Environment:
#   GATEWAY_URL     default http://localhost:8080
#   SMOKE_EMAIL     default mira@kestrel-lab.example (the seed's workspace owner)
#   SMOKE_PASSWORD  required: the password identity's seed loader gives seed users
#   SMOKE_WORKSPACE default 019c8cf2-1000-769b-b000-73aede463976 (Kestrel Lab Engineering)
#
# Needs curl and python3. Exits non-zero on the first failed check. It
# creates one note (titled "gateway smoke <timestamp>") in the workspace.
set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
SMOKE_EMAIL="${SMOKE_EMAIL:-mira@kestrel-lab.example}"
SMOKE_WORKSPACE="${SMOKE_WORKSPACE:-019c8cf2-1000-769b-b000-73aede463976}"
: "${SMOKE_PASSWORD:?set SMOKE_PASSWORD to the password of the seed users}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok   $*"; }

# call METHOD PATH [BODY] [extra curl args...]: writes body to $work/body and
# headers to $work/headers, prints the status code.
call() {
  local method="$1" path="$2" body="${3:-}"
  shift 3 || shift $#
  local args=(-sS -o "$work/body" -D "$work/headers" -w '%{http_code}' -X "$method" "$GATEWAY_URL$path")
  [[ -n "${TOKEN:-}" ]] && args+=(-H "Authorization: Bearer $TOKEN")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data "$body")
  curl "${args[@]}" "$@"
}

header() { grep -i "^$1:" "$work/headers" | tail -1 | cut -d' ' -f2- | tr -d '\r'; }
json() { python3 -c "import json,sys; d=json.load(open('$work/body')); print(eval(sys.argv[1]))" "$1"; }

[[ "$(call GET /healthz)" == 200 ]] || fail "healthz"
ok "healthz"
[[ "$(call GET /readyz)" == 200 ]] || fail "readyz: $(cat "$work/body")"
ok "readyz"
[[ "$(call GET /openapi.json)" == 200 ]] && [[ "$(json "d['info']['title']")" == "Skein API" ]] || fail "openapi.json"
ok "openapi.json"

code="$(call GET "/v1/workspaces/$SMOKE_WORKSPACE/notes")"
[[ "$code" == 401 ]] || fail "unauthenticated list should be 401, got $code"
[[ "$(json "d['code']")" == 16 ]] || fail "401 body is not a google.rpc.Status: $(cat "$work/body")"
[[ -n "$(header X-Request-Id)" ]] || fail "no X-Request-Id"
ok "401 is a google.rpc.Status with request ID $(header X-Request-Id)"

code="$(call POST /v1/sessions "{\"email\":\"$SMOKE_EMAIL\",\"password\":\"$SMOKE_PASSWORD\"}")"
[[ "$code" == 200 ]] || fail "sign in: $code $(cat "$work/body")"
TOKEN="$(json "d['session']['accessToken']")"
USER_ID="$(json "d['user']['id']")"
ok "signed in as $SMOKE_EMAIL ($USER_ID)"

[[ "$(call GET "/v1/users/$USER_ID")" == 200 ]] || fail "get user: $(cat "$work/body")"
ok "GET /v1/users/{userId}"

[[ "$(call GET "/v1/workspaces/$SMOKE_WORKSPACE/notes?page.pageSize=5")" == 200 ]] || fail "list notes: $(cat "$work/body")"
[[ -n "$(header RateLimit-Limit)" && -n "$(header RateLimit-Remaining)" ]] || fail "no RateLimit headers"
ok "list notes ($(json "len(d.get('notes', []))") notes; RateLimit-Remaining $(header RateLimit-Remaining))"

[[ "$(call GET "/v1/workspaces/$SMOKE_WORKSPACE/search?query=raft&page.pageSize=5")" == 200 ]] || fail "search: $(cat "$work/body")"
ok "search (budget $(header RateLimit-Limit))"

[[ "$(call GET "/v1/notifications?unreadOnly=true")" == 200 ]] || fail "inbox: $(cat "$work/body")"
ok "notifications inbox"

key="smoke-$(date +%s)-$$"
body="{\"title\":\"gateway smoke $(date -u +%Y-%m-%dT%H:%M:%SZ)\"}"
[[ "$(call POST "/v1/workspaces/$SMOKE_WORKSPACE/notes" "$body" -H "Idempotency-Key: $key")" == 200 ]] || fail "create note: $(cat "$work/body")"
first="$(json "d['note']['id']")"
[[ "$(call POST "/v1/workspaces/$SMOKE_WORKSPACE/notes" "$body" -H "Idempotency-Key: $key")" == 200 ]] || fail "retry: $(cat "$work/body")"
[[ "$(header Idempotent-Replayed)" == "true" && "$(json "d['note']['id']")" == "$first" ]] || fail "retry was not replayed"
code="$(call POST "/v1/workspaces/$SMOKE_WORKSPACE/notes" '{"title":"different"}' -H "Idempotency-Key: $key")"
[[ "$code" == 400 ]] || fail "key reuse with another body should be 400, got $code"
ok "idempotent create (note $first), replay and conflict"

echo "smoke passed against $GATEWAY_URL"
