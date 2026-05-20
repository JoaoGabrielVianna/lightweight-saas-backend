#!/usr/bin/env bash
# security_live_check.sh — black-box validation of the live API's security guards.
#
# Probes the running stack (docker-compose) to confirm that:
#   - public surfaces respond without credentials,
#   - protected routes reject missing / malformed / tampered tokens,
#   - role-gated routes reject authenticated callers that lack the role,
#   - admin tokens reach admin routes,
#   - the /admin static asset handler refuses path-traversal probes.
#
# Each probe writes one .txt evidence file under EVIDENCE_DIR. The script
# exits non-zero if any expected status code does not match what the server
# returned, so it is safe to wire into CI.
#
# Requirements: bash 4+, curl, jq.
# Owns only: scripts/security_live_check.sh, docs/evidence/security/**.
# Does not touch internal/** or web/**.

set -u
set -o pipefail

# ─── Configuration ──────────────────────────────────────────────────────────
API_URL="${API_URL:-http://localhost:8080}"
KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8081}"
REALM="${KEYCLOAK_REALM:-saas}"
CLIENT_ID="${KEYCLOAK_CLIENT_ID:-saas-backend}"
CLIENT_SECRET="${KEYCLOAK_CLIENT_SECRET:-saas-backend-secret}"
USER_USERNAME="${USER_USERNAME:-testuser}"
USER_PASSWORD="${USER_PASSWORD:-password}"
ADMIN_USERNAME="${ADMIN_USERNAME:-adminuser}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-password}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EVIDENCE_DIR="${EVIDENCE_DIR:-$REPO_ROOT/docs/evidence/security}"
CHECKS_DIR="$EVIDENCE_DIR/checks"
SUMMARY_FILE="$EVIDENCE_DIR/summary.txt"

mkdir -p "$CHECKS_DIR"
: >"$SUMMARY_FILE"

PASS_COUNT=0
FAIL_COUNT=0
FAILED_IDS=()

# ─── Helpers ────────────────────────────────────────────────────────────────

# log_summary <line>
log_summary() {
  printf '%s\n' "$1" | tee -a "$SUMMARY_FILE"
}

# require_tool <name>
require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "ERROR: required tool '$1' not on PATH" >&2
    exit 2
  fi
}

# fetch_token <username> <password> → echoes access_token
fetch_token() {
  local user="$1" pw="$2"
  curl -fsS -X POST "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "client_id=$CLIENT_ID" \
    -d "client_secret=$CLIENT_SECRET" \
    -d "grant_type=password" \
    -d "username=$user" \
    -d "password=$pw" \
  | jq -r '.access_token'
}

# run_check <id> <description> <expected_status> <curl-args...>
#
# Executes the curl invocation, captures status code + headers + body,
# writes them to EVIDENCE_DIR/checks/<id>.txt, and updates pass/fail counters.
run_check() {
  local id="$1" desc="$2" expected="$3"
  shift 3
  local outfile="$CHECKS_DIR/${id}.txt"
  local body_file headers_file
  body_file="$(mktemp)"
  headers_file="$(mktemp)"

  local actual
  actual=$(curl -sS -o "$body_file" -D "$headers_file" -w "%{http_code}" "$@" || true)

  {
    echo "# ${id} — ${desc}"
    echo "expected_status: ${expected}"
    echo "actual_status:   ${actual}"
    echo "curl_args:       $*"
    echo
    echo "--- response headers ---"
    cat "$headers_file"
    echo
    echo "--- response body (truncated to 2KB) ---"
    head -c 2048 "$body_file"
    echo
  } >"$outfile"

  if [ "$actual" = "$expected" ]; then
    PASS_COUNT=$((PASS_COUNT + 1))
    log_summary "PASS  ${id}  expected=${expected} actual=${actual}  ${desc}"
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
    FAILED_IDS+=("$id")
    log_summary "FAIL  ${id}  expected=${expected} actual=${actual}  ${desc}"
  fi

  rm -f "$body_file" "$headers_file"
}

# ─── Pre-flight ─────────────────────────────────────────────────────────────
require_tool curl
require_tool jq

log_summary "security_live_check — $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
log_summary "api=${API_URL}  keycloak=${KEYCLOAK_URL}  realm=${REALM}  client=${CLIENT_ID}"
log_summary ""

# Stack readiness — fail loudly rather than producing noise.
if ! curl -fsS -o /dev/null "$API_URL/health"; then
  log_summary "ABORT — API health probe failed at $API_URL/health"
  exit 2
fi
if ! curl -fsS -o /dev/null "$KEYCLOAK_URL/realms/$REALM/.well-known/openid-configuration"; then
  log_summary "ABORT — Keycloak realm discovery failed at $KEYCLOAK_URL/realms/$REALM/.well-known/openid-configuration"
  exit 2
fi

USER_TOKEN="$(fetch_token "$USER_USERNAME" "$USER_PASSWORD")"
ADMIN_TOKEN="$(fetch_token "$ADMIN_USERNAME" "$ADMIN_PASSWORD")"
if [ -z "$USER_TOKEN" ] || [ "$USER_TOKEN" = "null" ]; then
  log_summary "ABORT — could not fetch user token for $USER_USERNAME"
  exit 2
fi
if [ -z "$ADMIN_TOKEN" ] || [ "$ADMIN_TOKEN" = "null" ]; then
  log_summary "ABORT — could not fetch admin token for $ADMIN_USERNAME"
  exit 2
fi
log_summary "tokens acquired: user(len=${#USER_TOKEN}) admin(len=${#ADMIN_TOKEN})"
log_summary ""

# Forge a tampered token: keep header+payload, swap signature for junk bytes.
HDR="$(printf '%s' "$USER_TOKEN" | cut -d. -f1)"
PAY="$(printf '%s' "$USER_TOKEN" | cut -d. -f2)"
TAMPERED_TOKEN="${HDR}.${PAY}.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

# ─── Guard checks ───────────────────────────────────────────────────────────

# G01–G03: Public surfaces — must respond without credentials.
run_check G01 "GET /health (public)"                         200 "$API_URL/health"
run_check G02 "GET /swagger/index.html (public)"             200 "$API_URL/swagger/index.html"
run_check G03 "GET /admin (HTML shell, public; actions gated)" 200 "$API_URL/admin"

# G04–G05: Protected routes reject missing Authorization header.
run_check G04 "GET /me without Authorization → 401"          401 "$API_URL/me"
run_check G05 "GET /admin/users without Authorization → 401" 401 "$API_URL/admin/users"

# G06–G09: Malformed / forged Authorization headers all rejected.
run_check G06 "GET /me with empty Bearer token → 401"        401 -H "Authorization: Bearer " "$API_URL/me"
run_check G07 "GET /me without Bearer prefix → 401"          401 -H "Authorization: $USER_TOKEN" "$API_URL/me"
run_check G08 "GET /me with non-JWT garbage token → 401"     401 -H "Authorization: Bearer not.a.jwt" "$API_URL/me"
run_check G09 "GET /me with tampered signature → 401"        401 -H "Authorization: Bearer $TAMPERED_TOKEN" "$API_URL/me"

# G10–G12: Happy paths confirm guards aren't false-positives.
run_check G10 "GET /me with valid user token → 200"          200 -H "Authorization: Bearer $USER_TOKEN"  "$API_URL/me"
run_check G11 "GET /admin/users with user (no admin role) → 403" 403 -H "Authorization: Bearer $USER_TOKEN"  "$API_URL/admin/users"
run_check G12 "GET /admin/users with admin token → 200"      200 -H "Authorization: Bearer $ADMIN_TOKEN" "$API_URL/admin/users"

# G13: Path-traversal on the admin static asset handler is refused (403).
# --path-as-is prevents curl from collapsing ../ client-side, so the server
# actually sees the traversal segments.
run_check G13 "GET /admin/static/../../../etc/passwd → 403"  403 --path-as-is "$API_URL/admin/static/../../../etc/passwd"

# G14–G16: Mutating admin verbs require both auth and admin role.
run_check G14 "DELETE /admin/users/1 without Authorization → 401" 401 -X DELETE "$API_URL/admin/users/1"
run_check G15 "POST /admin/roles without Authorization → 401"     401 -X POST -H "Content-Type: application/json" -d '{}' "$API_URL/admin/roles"
run_check G16 "POST /admin/roles with user (no admin role) → 403" 403 -X POST -H "Authorization: Bearer $USER_TOKEN" -H "Content-Type: application/json" -d '{}' "$API_URL/admin/roles"

# G17: A subject-less raw string (no JWT structure) is rejected.
run_check G17 "GET /me with single-segment token → 401"      401 -H "Authorization: Bearer abc" "$API_URL/me"

# ─── Summary ────────────────────────────────────────────────────────────────
log_summary ""
log_summary "─────────────────────────────────────────────────────────────"
log_summary "TOTAL: $((PASS_COUNT + FAIL_COUNT))   PASS: ${PASS_COUNT}   FAIL: ${FAIL_COUNT}"
if [ "$FAIL_COUNT" -gt 0 ]; then
  log_summary "FAILED: ${FAILED_IDS[*]}"
  log_summary "Result: FAIL"
  exit 1
fi
log_summary "Result: PASS"
exit 0
