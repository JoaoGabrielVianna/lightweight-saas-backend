#!/usr/bin/env bash
# security_gap1_check.sh — focused live-stack validation of the GAP-1
# remediation (docs/security/SECURITY_REMEDIATION_GAP1.md).
#
# What this script proves:
#
#   G1.1  pre-revoke baseline — admin token can list /admin/users         (200)
#   G1.2  grant testuser admin via API                                    (204)
#   G1.3  fresh testuser token can list /admin/users                      (200)
#   G1.4  fresh testuser token can PATCH another user (admin verb)        (200)
#   G1.5  revoke testuser admin via API                                   (204)
#   G1.6  STALE testuser token can NO LONGER list /admin/users            (403)
#   G1.7  STALE testuser token can NO LONGER PATCH another user           (403)
#   G1.8  current admin (untouched) still works                           (200)
#   G1.9  normal user (no role grant) still denied                        (403)
#   G1.10 /me still works for the demoted user (auth unaffected)          (200)
#
# Steps G1.6/G1.7 are the GAP-1 attack flow from docs/security/SECURITY_GAPS.md §D.
# Before this remediation they returned 200 (exploit). After: 403.
#
# Evidence: one file per test under docs/evidence/security/gaps/remediation/.
# Summary roll-up: docs/evidence/security/gaps/remediation/summary.txt.
#
# This script is read-only against the realm at end-of-run: every state
# change (grant/revoke admin on testuser, PATCH adminuser, etc.) is rolled
# back inline, and the final state is verified against the initial snapshot.

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
EVIDENCE_DIR="${EVIDENCE_DIR:-$REPO_ROOT/docs/evidence/security/gaps/remediation}"
SUMMARY_FILE="$EVIDENCE_DIR/summary.txt"

PASS_COUNT=0
FAIL_COUNT=0
FAILED_IDS=()

mkdir -p "$EVIDENCE_DIR"
: >"$SUMMARY_FILE"

# ─── Helpers ────────────────────────────────────────────────────────────────
log() { printf '%s\n' "$1" | tee -a "$SUMMARY_FILE"; }

require_tool() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: '$1' not on PATH" >&2; exit 2; }
}

fetch_token() {
  curl -fsS -X POST "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "client_id=$CLIENT_ID" \
    -d "client_secret=$CLIENT_SECRET" \
    -d "grant_type=password" \
    -d "scope=openid" \
    -d "username=$1" \
    -d "password=$2"
}

# verdict <id> <result> <description>
verdict() {
  case "$2" in
    PASS) PASS_COUNT=$((PASS_COUNT+1)) ;;
    FAIL) FAIL_COUNT=$((FAIL_COUNT+1)); FAILED_IDS+=("$1") ;;
  esac
  log "${2}  ${1}  ${3}"
}

# expect_status <id> <expected-code> <description> <evidence-file> <curl-args...>
expect_status() {
  local id="$1" expect="$2" desc="$3" evidence="$4"
  shift 4
  local actual
  actual=$(curl -sS -o "${evidence%.txt}.body.txt" -D "$evidence" -w "%{http_code}" "$@")
  printf '\n## %s\n## expected=%s actual=%s\n' "$desc" "$expect" "$actual" >>"$evidence"
  if [ "$actual" = "$expect" ]; then
    verdict "$id" PASS "$desc (expected=$expect actual=$actual)"
  else
    verdict "$id" FAIL "$desc (expected=$expect actual=$actual) — see $(basename "$evidence")"
  fi
}

# ─── Pre-flight ─────────────────────────────────────────────────────────────
require_tool curl
require_tool jq

log "security_gap1_check — $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
log "api=${API_URL}  keycloak=${KEYCLOAK_URL}  realm=${REALM}  client=${CLIENT_ID}"
log ""

if ! curl -fsS -o /dev/null "$API_URL/health"; then
  log "ABORT — $API_URL/health unreachable"; exit 2
fi
if ! curl -fsS -o /dev/null "$KEYCLOAK_URL/realms/$REALM/.well-known/openid-configuration"; then
  log "ABORT — Keycloak realm discovery unreachable"; exit 2
fi

ADMIN_TOKEN=$(fetch_token "$ADMIN_USERNAME" "$ADMIN_PASSWORD" | jq -r .access_token)
[ -z "$ADMIN_TOKEN" ] || [ "$ADMIN_TOKEN" = "null" ] && { log "ABORT — no admin token"; exit 2; }

USER_TOKEN_PRE=$(fetch_token "$USER_USERNAME" "$USER_PASSWORD" | jq -r .access_token)
[ -z "$USER_TOKEN_PRE" ] || [ "$USER_TOKEN_PRE" = "null" ] && { log "ABORT — no user token"; exit 2; }

# Resolve UUIDs from the admin listing — adminuser/testuser/etc.
USERS_JSON=$(curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" "$API_URL/admin/users")
ADMIN_ID=$(echo "$USERS_JSON" | jq -r --arg u "$ADMIN_USERNAME" '.users[] | select(.username==$u) | .id' | head -1)
TEST_ID=$(echo "$USERS_JSON"  | jq -r --arg u "$USER_USERNAME"  '.users[] | select(.username==$u) | .id' | head -1)
if [ -z "$ADMIN_ID" ] || [ -z "$TEST_ID" ]; then
  log "ABORT — could not resolve user ids (admin=${ADMIN_ID} test=${TEST_ID})"
  exit 2
fi
log "resolved ids: admin=${ADMIN_ID} test=${TEST_ID}"
log ""

# Snapshot adminuser's first_name so we can compare end-of-run.
ADMIN_BEFORE=$(curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" "$API_URL/admin/users/$ADMIN_ID" | jq -r .first_name)
log "snapshot: adminuser.first_name=${ADMIN_BEFORE}"
log ""

# Cleanup trap — best-effort rollback even on abnormal exit.
cleanup() {
  local code=$?
  # Best-effort restore adminuser.first_name (no-op if unchanged).
  curl -sS -o /dev/null -X PATCH -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"first_name\":\"${ADMIN_BEFORE}\"}" "$API_URL/admin/users/$ADMIN_ID" || true
  # Best-effort revoke admin from testuser if the test left it granted.
  curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN" \
    "$API_URL/admin/users/$TEST_ID/roles/admin" || true
  exit "$code"
}
trap cleanup EXIT

# ─── G1.1 — baseline ────────────────────────────────────────────────────────
expect_status G1.1 200 "baseline: admin can GET /admin/users" \
  "$EVIDENCE_DIR/G1.1_admin_baseline_headers.txt" \
  -H "Authorization: Bearer $ADMIN_TOKEN" "$API_URL/admin/users"

# ─── G1.9 — normal user still denied (pre-grant) ────────────────────────────
expect_status G1.9 403 "normal user still denied on /admin/users (no admin role)" \
  "$EVIDENCE_DIR/G1.9_normal_user_denied_headers.txt" \
  -H "Authorization: Bearer $USER_TOKEN_PRE" "$API_URL/admin/users"

# ─── G1.2 — grant testuser admin ────────────────────────────────────────────
expect_status G1.2 204 "grant testuser admin via /admin/users/{id}/roles" \
  "$EVIDENCE_DIR/G1.2_grant_admin_headers.txt" \
  -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"roles":["admin"]}' \
  "$API_URL/admin/users/$TEST_ID/roles"

# Mint a FRESH testuser token AFTER the grant. This token carries the admin
# claim — the GAP-1 attacker's stolen token.
USER_TOKEN_ADMIN=$(fetch_token "$USER_USERNAME" "$USER_PASSWORD" | jq -r .access_token)
[ -z "$USER_TOKEN_ADMIN" ] || [ "$USER_TOKEN_ADMIN" = "null" ] && { log "ABORT — no post-grant testuser token"; exit 2; }
log ""
log "minted post-grant testuser token (len=${#USER_TOKEN_ADMIN})"

# ─── G1.3 — fresh testuser token can list /admin/users ──────────────────────
expect_status G1.3 200 "freshly-promoted testuser can list /admin/users" \
  "$EVIDENCE_DIR/G1.3_promoted_list_headers.txt" \
  -H "Authorization: Bearer $USER_TOKEN_ADMIN" "$API_URL/admin/users"

# ─── G1.4 — fresh testuser token can PATCH another user (admin verb) ───────
expect_status G1.4 200 "freshly-promoted testuser can PATCH adminuser.first_name" \
  "$EVIDENCE_DIR/G1.4_promoted_patch_headers.txt" \
  -X PATCH -H "Authorization: Bearer $USER_TOKEN_ADMIN" \
  -H "Content-Type: application/json" \
  -d '{"first_name":"PROMOTED-OK"}' \
  "$API_URL/admin/users/$ADMIN_ID"

# Restore adminuser.first_name immediately (do not depend on cleanup trap).
curl -sS -o /dev/null -X PATCH -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"first_name\":\"${ADMIN_BEFORE}\"}" "$API_URL/admin/users/$ADMIN_ID" || true

# ─── G1.5 — revoke testuser admin ───────────────────────────────────────────
expect_status G1.5 204 "revoke testuser admin via DELETE /admin/users/{id}/roles/admin" \
  "$EVIDENCE_DIR/G1.5_revoke_admin_headers.txt" \
  -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN" \
  "$API_URL/admin/users/$TEST_ID/roles/admin"

# Confirm server state: testuser should no longer be in admin role members.
ADMIN_MEMBERS=$(curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" "$API_URL/admin/roles/admin/users" | jq -r '.users[].id' | tr '\n' ' ')
log "post-revoke admin role members: ${ADMIN_MEMBERS}"
if echo " $ADMIN_MEMBERS " | grep -q " $TEST_ID "; then
  verdict G1.5.server_state FAIL "testuser still in admin role membership after revoke"
else
  verdict G1.5.server_state PASS "testuser removed from admin role membership server-side"
fi

# ─── G1.6 — STALE testuser token rejected on /admin/users ───────────────────
# This is the GAP-1 core exploit, post-fix.
expect_status G1.6 403 "STALE testuser token rejected on GET /admin/users (GAP-1 closed)" \
  "$EVIDENCE_DIR/G1.6_stale_token_list_headers.txt" \
  -H "Authorization: Bearer $USER_TOKEN_ADMIN" "$API_URL/admin/users"

# ─── G1.7 — STALE testuser token rejected on PATCH ──────────────────────────
expect_status G1.7 403 "STALE testuser token rejected on PATCH /admin/users/{adminuser}" \
  "$EVIDENCE_DIR/G1.7_stale_token_patch_headers.txt" \
  -X PATCH -H "Authorization: Bearer $USER_TOKEN_ADMIN" \
  -H "Content-Type: application/json" \
  -d '{"first_name":"PWNED"}' \
  "$API_URL/admin/users/$ADMIN_ID"

# Confirm adminuser.first_name was NOT mutated by the rejected PATCH.
ADMIN_AFTER=$(curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" "$API_URL/admin/users/$ADMIN_ID" | jq -r .first_name)
if [ "$ADMIN_AFTER" = "$ADMIN_BEFORE" ]; then
  verdict G1.7.no_mutation PASS "adminuser.first_name unchanged after rejected PATCH (was=${ADMIN_BEFORE}, is=${ADMIN_AFTER})"
else
  verdict G1.7.no_mutation FAIL "adminuser.first_name MUTATED despite 403 (was=${ADMIN_BEFORE}, is=${ADMIN_AFTER})"
fi

# ─── G1.8 — current admin still works ───────────────────────────────────────
expect_status G1.8 200 "current admin (adminuser) still passes /admin/users" \
  "$EVIDENCE_DIR/G1.8_current_admin_headers.txt" \
  -H "Authorization: Bearer $ADMIN_TOKEN" "$API_URL/admin/users"

# ─── G1.10 — /me still works for the demoted user ───────────────────────────
expect_status G1.10 200 "/me still works for the demoted user (auth unaffected)" \
  "$EVIDENCE_DIR/G1.10_me_demoted_headers.txt" \
  -H "Authorization: Bearer $USER_TOKEN_ADMIN" "$API_URL/me"

# ─── Summary ────────────────────────────────────────────────────────────────
log ""
log "─────────────────────────────────────────────────────────────"
log "TOTAL: $((PASS_COUNT + FAIL_COUNT))   PASS: ${PASS_COUNT}   FAIL: ${FAIL_COUNT}"
if [ "$FAIL_COUNT" -gt 0 ]; then
  log "FAILED: ${FAILED_IDS[*]}"
  log "Result: FAIL — GAP-1 still exploitable or regression introduced"
  exit 1
fi
log "Result: PASS — GAP-1 closed"
exit 0
