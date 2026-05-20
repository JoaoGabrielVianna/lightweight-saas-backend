#!/usr/bin/env bash
# security_advanced_check.sh — advanced security probes against the live stack.
#
# Six threat surfaces are exercised:
#
#   T1  rate limiting           — burst /me, /admin/users, /health, and the
#                                 Keycloak token endpoint; report status
#                                 distributions (informational; no PASS/FAIL).
#   T2  brute force             — confirm Keycloak's bruteForceProtected
#                                 lockout triggers after ~30 failed logins
#                                 and that the admin API can clear it.
#   T3  session fixation        — confirm an access token still works against
#                                 /me after the user logs out via OIDC, while
#                                 the paired refresh token IS invalidated.
#                                 (Recorded as a deliberate JWT trade-off.)
#   T4  token replay            — confirm the same valid token is accepted
#                                 across many sequential + parallel requests
#                                 (no per-request nonce by design).
#   T5  concurrent admin        — fire 10 parallel POST /admin/roles with the
#                                 same name; expect exactly 1×201 + 9×409.
#   T6  privilege escalation    — every admin verb with a non-admin token,
#                                 plus header injection and cross-client
#                                 tokens — all must be denied.
#
# Evidence: one file per test under docs/evidence/security/advanced/.
# Summary roll-up: docs/evidence/security/advanced/summary.txt.
#
# IMPORTANT: T2 deliberately locks `testuser` out via Keycloak's brute-force
# tracker. The script unlocks it at the end (and on EXIT trap) via the
# Keycloak master-realm admin API. If the master admin credentials are wrong
# or the script is killed -9, `testuser` may remain temporarily disabled —
# run `make realm-reset` or call DELETE /admin/realms/saas/attack-detection/
# brute-force/users manually to recover.
#
# Owns only: scripts/security_advanced_check.sh, docs/evidence/security/advanced/**.
# Does not touch internal/** or web/**.

set -u
set -o pipefail

# ─── Configuration ──────────────────────────────────────────────────────────
API_URL="${API_URL:-http://localhost:8080}"
KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8081}"
REALM="${KEYCLOAK_REALM:-saas}"
CLIENT_ID="${KEYCLOAK_CLIENT_ID:-saas-backend}"
CLIENT_SECRET="${KEYCLOAK_CLIENT_SECRET:-saas-backend-secret}"
ADMIN_CLIENT_ID="${KEYCLOAK_ADMIN_CLIENT_ID:-saas-backend-admin}"
ADMIN_CLIENT_SECRET="${KEYCLOAK_ADMIN_CLIENT_SECRET:-saas-backend-admin-secret}"
USER_USERNAME="${USER_USERNAME:-testuser}"
USER_PASSWORD="${USER_PASSWORD:-password}"
ADMIN_USERNAME="${ADMIN_USERNAME:-adminuser}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-password}"
KC_MASTER_USER="${KEYCLOAK_ADMIN:-admin}"
KC_MASTER_PASS="${KEYCLOAK_ADMIN_PASSWORD:-admin}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EVIDENCE_DIR="${EVIDENCE_DIR:-$REPO_ROOT/docs/evidence/security/advanced}"
SUMMARY_FILE="$EVIDENCE_DIR/summary.txt"

# Tunables — bias toward exercising the guards without being noisy.
BURST_ME=100
BURST_ADMIN=50
BURST_HEALTH=200
BURST_KC=50
BURST_PARALLEL=50
BRUTE_ATTEMPTS=35       # default Keycloak failureFactor=30 — go past it
REPLAY_SEQUENTIAL=10
REPLAY_PARALLEL=30
CONCURRENT_ADMIN=10

mkdir -p "$EVIDENCE_DIR"
: >"$SUMMARY_FILE"

PASS_COUNT=0
FAIL_COUNT=0
INFO_COUNT=0
FAILED_IDS=()

# ─── Helpers ────────────────────────────────────────────────────────────────
log()  { printf '%s\n' "$1" | tee -a "$SUMMARY_FILE"; }

require_tool() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: '$1' not on PATH" >&2; exit 2; }
}

# fetch_token <user> <pw>
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

# fetch_master_token — Keycloak master realm admin token (admin-cli)
fetch_master_token() {
  curl -fsS -X POST "$KEYCLOAK_URL/realms/master/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "client_id=admin-cli" \
    -d "username=$KC_MASTER_USER" \
    -d "password=$KC_MASTER_PASS" \
    -d "grant_type=password" \
  | jq -r .access_token
}

# clear_brute_force — wipe Keycloak's brute-force tracker for the realm.
clear_brute_force() {
  local mt
  mt=$(fetch_master_token 2>/dev/null || echo "")
  if [ -z "$mt" ] || [ "$mt" = "null" ]; then return 1; fi
  curl -sS -o /dev/null -w "%{http_code}" \
    -X DELETE -H "Authorization: Bearer $mt" \
    "$KEYCLOAK_URL/admin/realms/$REALM/attack-detection/brute-force/users"
}

# verdict <id> <result PASS|FAIL|INFO> <description>
verdict() {
  case "$2" in
    PASS) PASS_COUNT=$((PASS_COUNT+1)) ;;
    FAIL) FAIL_COUNT=$((FAIL_COUNT+1)); FAILED_IDS+=("$1") ;;
    INFO) INFO_COUNT=$((INFO_COUNT+1)) ;;
  esac
  log "${2}  ${1}  ${3}"
}

# burst <count> <parallelism> <evidence-file> <label> <curl-args...>
#
# Runs `count` curls in parallel and reports the distribution of status codes.
# Outputs the histogram to the evidence file and returns the dominant code on stdout.
burst() {
  local count="$1" para="$2" out="$3" label="$4"
  shift 4
  local args=("$@")
  local tmp; tmp=$(mktemp)
  # shellcheck disable=SC2016
  seq "$count" | xargs -P "$para" -I {} sh -c '
    code=$(curl -sS -o /dev/null -w "%{http_code}" "$@")
    printf "%s\n" "$code"
  ' _ "${args[@]}" >"$tmp" 2>/dev/null
  {
    echo "# ${label}"
    echo "## status-code distribution (count=${count}, parallelism=${para}):"
    sort "$tmp" | uniq -c | sort -nr
    echo ""
    echo "## first 30 raw responses (chronological):"
    head -n 30 "$tmp"
  } >"$out"
  # Echo dominant code for verdict logic.
  sort "$tmp" | uniq -c | sort -nr | awk 'NR==1{print $2}'
  rm -f "$tmp"
}

# parallel_post <count> <parallelism> <out> <label> <-- followed by curl args -->
#
# Same as burst() but each curl is the same POST request — used to fire
# concurrent admin actions where we want to count outcomes.
parallel_post() {
  local count="$1" para="$2" out="$3" label="$4"
  shift 4
  local tmp; tmp=$(mktemp)
  # shellcheck disable=SC2016
  seq "$count" | xargs -P "$para" -I {} sh -c '
    code=$(curl -sS -o /dev/null -w "%{http_code}" "$@")
    printf "%s\n" "$code"
  ' _ "$@" >"$tmp" 2>/dev/null
  {
    echo "# ${label}"
    echo "## status-code distribution (count=${count}, parallelism=${para}):"
    sort "$tmp" | uniq -c | sort -nr
  } >"$out"
  sort "$tmp" | uniq -c | sort -nr
  rm -f "$tmp"
}

# ─── Pre-flight ─────────────────────────────────────────────────────────────
require_tool curl
require_tool jq
require_tool xargs

log "security_advanced_check — $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
log "api=${API_URL}  keycloak=${KEYCLOAK_URL}  realm=${REALM}  client=${CLIENT_ID}"
log ""

if ! curl -fsS -o /dev/null "$API_URL/health"; then
  log "ABORT — $API_URL/health unreachable"; exit 2
fi
if ! curl -fsS -o /dev/null "$KEYCLOAK_URL/realms/$REALM/.well-known/openid-configuration"; then
  log "ABORT — Keycloak realm discovery unreachable"; exit 2
fi

# Verify master admin works BEFORE we lock out the test user.
MASTER_TEST=$(fetch_master_token 2>/dev/null || echo "")
if [ -z "$MASTER_TEST" ] || [ "$MASTER_TEST" = "null" ]; then
  log "ABORT — cannot acquire Keycloak master admin token (admin-cli, ${KC_MASTER_USER}/${KC_MASTER_PASS}). Refusing to run T2 without an unlock path."
  exit 2
fi

# Pre-emptively clear any leftover lockouts from a prior run.
clear_brute_force >/dev/null || true

USER_TOKEN_BUNDLE=$(fetch_token "$USER_USERNAME" "$USER_PASSWORD")
USER_TOKEN=$(echo "$USER_TOKEN_BUNDLE" | jq -r .access_token)
USER_REFRESH=$(echo "$USER_TOKEN_BUNDLE" | jq -r .refresh_token)
ADMIN_TOKEN=$(fetch_token "$ADMIN_USERNAME" "$ADMIN_PASSWORD" | jq -r .access_token)
[ -z "$USER_TOKEN"  ] || [ "$USER_TOKEN"  = "null" ] && { log "ABORT — no user token";  exit 2; }
[ -z "$ADMIN_TOKEN" ] || [ "$ADMIN_TOKEN" = "null" ] && { log "ABORT — no admin token"; exit 2; }
log "tokens acquired: user(len=${#USER_TOKEN}) admin(len=${#ADMIN_TOKEN}) refresh(len=${#USER_REFRESH})"

# Make tokens visible to xargs-spawned sub-shells.
export USER_TOKEN ADMIN_TOKEN

# Unlock on any exit — even abnormal — so the next run starts clean.
trap 'clear_brute_force >/dev/null 2>&1 || true' EXIT

# ─── T1 — Rate limiting (informational) ─────────────────────────────────────
log ""
log "── T1 — Rate limiting probe ─────────────────────────────────────"

dom_me=$(burst "$BURST_ME" "$BURST_PARALLEL" \
  "$EVIDENCE_DIR/T1a_burst_me_authed.txt" \
  "T1a — /me x${BURST_ME} parallel with valid user token" \
  -H "Authorization: Bearer $USER_TOKEN" "$API_URL/me")
log "T1a  /me authed       dominant=${dom_me}  (expect 200; no API rate limit claimed)"
[ "$dom_me" = "200" ] && verdict T1a INFO "no rate limit on /me with valid token (recorded)" \
                       || verdict T1a INFO "unexpected dominant code ${dom_me} on /me burst"

dom_admin=$(burst "$BURST_ADMIN" "$BURST_PARALLEL" \
  "$EVIDENCE_DIR/T1b_burst_admin_unauth.txt" \
  "T1b — /admin/users x${BURST_ADMIN} parallel no token" \
  "$API_URL/admin/users")
log "T1b  /admin unauthed  dominant=${dom_admin}  (expect 401)"
verdict T1b INFO "no rate limit on unauthenticated /admin/users; consistent 401 (${dom_admin})"

dom_health=$(burst "$BURST_HEALTH" "$BURST_PARALLEL" \
  "$EVIDENCE_DIR/T1c_burst_health.txt" \
  "T1c — /health x${BURST_HEALTH} parallel" \
  "$API_URL/health")
log "T1c  /health          dominant=${dom_health}  (expect 200)"
verdict T1c INFO "no rate limit on /health (${dom_health})"

dom_kc=$(burst "$BURST_KC" "$BURST_PARALLEL" \
  "$EVIDENCE_DIR/T1d_burst_kc_token.txt" \
  "T1d — Keycloak token endpoint x${BURST_KC} parallel with bogus user" \
  -X POST -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=$CLIENT_ID" -d "client_secret=$CLIENT_SECRET" \
  -d "grant_type=password" -d "username=nobody-$$" -d "password=wrong" \
  "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token")
log "T1d  KC token burst   dominant=${dom_kc}  (Keycloak — informational)"
verdict T1d INFO "Keycloak token endpoint burst dominant=${dom_kc}"

# ─── T2 — Brute force protection ────────────────────────────────────────────
log ""
log "── T2 — Brute-force protection ──────────────────────────────────"

# Ensure we start fully unlocked.
clear_brute_force >/dev/null || true
sleep 1

# Control: CORRECT password must work before we begin.
pre=$(curl -sS -o /dev/null -w "%{http_code}" -X POST \
  "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=$CLIENT_ID" -d "client_secret=$CLIENT_SECRET" \
  -d "grant_type=password" -d "username=$USER_USERNAME" -d "password=$USER_PASSWORD")
log "T2 control: correct password before brute force → ${pre} (expect 200)"

# Hammer: BRUTE_ATTEMPTS wrong-password requests.
brute_out="$EVIDENCE_DIR/T2_brute_force_attempts.txt"
: >"$brute_out"
for i in $(seq 1 "$BRUTE_ATTEMPTS"); do
  code=$(curl -sS -o /dev/null -w "%{http_code}" -X POST \
    "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "client_id=$CLIENT_ID" -d "client_secret=$CLIENT_SECRET" \
    -d "grant_type=password" -d "username=$USER_USERNAME" -d "password=wrong-pw-$i")
  printf 'attempt=%d code=%s\n' "$i" "$code" >>"$brute_out"
done
log "T2 hammered ${BRUTE_ATTEMPTS} wrong-password attempts (see $(basename "$brute_out"))"

# Now CORRECT password should be rejected because the account is locked.
post=$(curl -sS -X POST \
  "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=$CLIENT_ID" -d "client_secret=$CLIENT_SECRET" \
  -d "grant_type=password" -d "username=$USER_USERNAME" -d "password=$USER_PASSWORD")
post_has_token=$(echo "$post" | jq -r '.access_token // empty' | wc -c | awk '{print $1}')
post_err=$(echo "$post" | jq -r '.error_description // .error // empty')
{
  echo "# T2 — correct-password attempt AFTER brute force"
  echo "response: $post"
  echo "has_access_token: $([ "$post_has_token" -gt 1 ] && echo yes || echo no)"
} >"$EVIDENCE_DIR/T2_post_brute_correct_pw.txt"

if [ "$post_has_token" -le 1 ]; then
  verdict T2.lockout PASS "Keycloak rejected correct password after ${BRUTE_ATTEMPTS} failed attempts (lockout active; error=\"${post_err}\")"
else
  verdict T2.lockout FAIL "Keycloak did NOT lock the account — bruteForceProtected appears INEFFECTIVE"
fi

# Clear and confirm recovery — proves the lockout is admin-recoverable.
clear_code=$(clear_brute_force)
log "T2 clear_brute_force returned http=${clear_code}"
sleep 1
recovery=$(curl -sS -o /dev/null -w "%{http_code}" -X POST \
  "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=$CLIENT_ID" -d "client_secret=$CLIENT_SECRET" \
  -d "grant_type=password" -d "username=$USER_USERNAME" -d "password=$USER_PASSWORD")
if [ "$recovery" = "200" ]; then
  verdict T2.recovery PASS "admin unlock restored login (${recovery})"
else
  verdict T2.recovery FAIL "after admin unlock, login still failing (${recovery})"
fi

# Re-issue a fresh user token for the remaining tests.
USER_TOKEN_BUNDLE=$(fetch_token "$USER_USERNAME" "$USER_PASSWORD")
USER_TOKEN=$(echo "$USER_TOKEN_BUNDLE" | jq -r .access_token)
USER_REFRESH=$(echo "$USER_TOKEN_BUNDLE" | jq -r .refresh_token)
export USER_TOKEN

# ─── T3 — Session fixation / post-logout token reuse ────────────────────────
log ""
log "── T3 — Session fixation / post-logout token reuse ──────────────"

t3_pre=$(curl -sS -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $USER_TOKEN" "$API_URL/me")
logout_code=$(curl -sS -o /dev/null -w "%{http_code}" -X POST \
  "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/logout" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=$CLIENT_ID" -d "client_secret=$CLIENT_SECRET" \
  -d "refresh_token=$USER_REFRESH")
t3_post=$(curl -sS -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $USER_TOKEN" "$API_URL/me")
refresh_after=$(curl -sS -X POST \
  "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=$CLIENT_ID" -d "client_secret=$CLIENT_SECRET" \
  -d "grant_type=refresh_token" -d "refresh_token=$USER_REFRESH")
refresh_has_token=$(echo "$refresh_after" | jq -r '.access_token // empty' | wc -c | awk '{print $1}')
refresh_err=$(echo "$refresh_after" | jq -r '.error_description // .error // empty')

{
  echo "# T3 — post-logout token reuse"
  echo "before_logout /me:    ${t3_pre}    (expect 200)"
  echo "logout endpoint http: ${logout_code}    (expect 204)"
  echo "after_logout  /me:    ${t3_post}    (JWT remains valid until expiry by design)"
  echo "refresh_token reuse:  has_access_token=$([ "$refresh_has_token" -gt 1 ] && echo yes || echo no)  error=\"${refresh_err}\""
  echo ""
  echo "NOTE: This is the documented JWT trade-off. Access tokens are stateless and"
  echo "remain valid until 'exp'. A defense-in-depth measure would require backchannel-"
  echo "logout listening or short-lived access tokens. The refresh token MUST be invalid."
} >"$EVIDENCE_DIR/T3_post_logout_token_reuse.txt"

if [ "$t3_pre" = "200" ] && [ "$logout_code" = "204" ] && [ "$t3_post" = "200" ] && [ "$refresh_has_token" -le 1 ]; then
  verdict T3.refresh PASS "refresh_token invalidated by OIDC logout (error=\"${refresh_err}\")"
  verdict T3.access  INFO "access_token STILL valid post-logout — JWT trade-off; documented finding"
elif [ "$refresh_has_token" -gt 1 ]; then
  verdict T3.refresh FAIL "refresh_token still works after logout — Keycloak misconfigured"
else
  verdict T3.refresh FAIL "T3 control failed (pre=${t3_pre} logout=${logout_code} post=${t3_post})"
fi

# ─── T4 — Token replay ──────────────────────────────────────────────────────
log ""
log "── T4 — Token replay ────────────────────────────────────────────"

t4_seq_file="$EVIDENCE_DIR/T4_replay_sequential.txt"
: >"$t4_seq_file"
seq_ok=0
for i in $(seq 1 "$REPLAY_SEQUENTIAL"); do
  c=$(curl -sS -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $USER_TOKEN" "$API_URL/me")
  printf 'replay=%d code=%s\n' "$i" "$c" >>"$t4_seq_file"
  [ "$c" = "200" ] && seq_ok=$((seq_ok+1))
done
log "T4a sequential replays: ${seq_ok}/${REPLAY_SEQUENTIAL} 200s"

dom_replay=$(burst "$REPLAY_PARALLEL" "$REPLAY_PARALLEL" \
  "$EVIDENCE_DIR/T4_replay_parallel.txt" \
  "T4b — /me x${REPLAY_PARALLEL} parallel, same token" \
  -H "Authorization: Bearer $USER_TOKEN" "$API_URL/me")

if [ "$seq_ok" -eq "$REPLAY_SEQUENTIAL" ] && [ "$dom_replay" = "200" ]; then
  verdict T4 INFO "token replays succeed (${seq_ok}/${REPLAY_SEQUENTIAL} seq, ${REPLAY_PARALLEL}x ${dom_replay} parallel) — expected behavior for bearer JWTs; no nonce/jti tracking implemented"
else
  verdict T4 FAIL "unexpected behavior on token replay (seq_ok=${seq_ok}, par_dom=${dom_replay})"
fi

# ─── T5 — Concurrent admin actions ──────────────────────────────────────────
log ""
log "── T5 — Concurrent admin actions ────────────────────────────────"

ROLE="sec-adv-$(date +%s)-$$"
t5_file="$EVIDENCE_DIR/T5_concurrent_admin_roles.txt"
log "T5 firing ${CONCURRENT_ADMIN}x POST /admin/roles with name=\"${ROLE}\" in parallel"
hist=$(parallel_post "$CONCURRENT_ADMIN" "$CONCURRENT_ADMIN" \
  "$t5_file" \
  "T5 — ${CONCURRENT_ADMIN} concurrent POST /admin/roles name=${ROLE}" \
  -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d "{\"name\":\"${ROLE}\",\"description\":\"sec-adv-probe\"}" \
  "$API_URL/admin/roles")
# Append the role name and history to the evidence.
{
  echo ""
  echo "## ROLE name used: ${ROLE}"
  echo "## raw histogram (count code):"
  echo "$hist"
} >>"$t5_file"

# Count 201s and 409s.
created=$(echo "$hist" | awk '$2=="201"{print $1}')
conflict=$(echo "$hist" | awk '$2=="409"{print $1}')
created=${created:-0}; conflict=${conflict:-0}
log "T5 outcome: 201=${created}  409=${conflict}  (expect 1 / $((CONCURRENT_ADMIN-1)))"
if [ "$created" = "1" ] && [ "$conflict" = "$((CONCURRENT_ADMIN-1))" ]; then
  verdict T5 PASS "race-safe: 1 create / $((CONCURRENT_ADMIN-1)) conflict"
else
  verdict T5 FAIL "expected 1×201 + $((CONCURRENT_ADMIN-1))×409, got 201=${created} 409=${conflict}"
fi

# Cleanup the role.
cleanup_code=$(curl -sS -o /dev/null -w "%{http_code}" -X DELETE \
  -H "Authorization: Bearer $ADMIN_TOKEN" "$API_URL/admin/roles/${ROLE}")
log "T5 cleanup DELETE /admin/roles/${ROLE} → ${cleanup_code}"

# ─── T6 — Privilege escalation ──────────────────────────────────────────────
log ""
log "── T6 — Privilege escalation ────────────────────────────────────"

t6_file="$EVIDENCE_DIR/T6_privilege_escalation.txt"
: >"$t6_file"
fake_uuid="00000000-0000-0000-0000-000000000000"
declare -a CHECKS=(
  "GET    /admin/roles                                                                                                                             403"
  "GET    /admin/users                                                                                                                             403"
  "GET    /admin/sessions                                                                                                                          403"
  "GET    /admin/invitations                                                                                                                       403"
  "POST   /admin/roles                                                                                                                             403"
  "POST   /admin/invitations                                                                                                                       403"
  "PATCH  /admin/users/${fake_uuid}                                                                                                                403"
  "PATCH  /admin/roles/admin                                                                                                                       403"
  "POST   /admin/users/${fake_uuid}/roles                                                                                                          403"
  "POST   /admin/users/${fake_uuid}/reset-password                                                                                                 403"
  "DELETE /admin/users/${fake_uuid}                                                                                                                403"
  "DELETE /admin/users/${fake_uuid}/roles/admin                                                                                                    403"
  "DELETE /admin/users/${fake_uuid}/sessions                                                                                                       403"
  "DELETE /admin/roles/admin                                                                                                                       403"
)

all_ok=1
{
  echo "# T6 — privilege escalation matrix"
  echo "## non-admin user attempting every admin verb"
  echo ""
} >>"$t6_file"

for line in "${CHECKS[@]}"; do
  # shellcheck disable=SC2086
  set -- $line
  method="$1"; path="$2"; expect="$3"
  actual=$(curl -sS -o /dev/null -w "%{http_code}" \
    -X "$method" -H "Authorization: Bearer $USER_TOKEN" \
    -H "Content-Type: application/json" -d '{}' \
    "$API_URL$path")
  printf '%-7s %-50s expected=%s actual=%s\n' "$method" "$path" "$expect" "$actual" >>"$t6_file"
  if [ "$actual" != "$expect" ]; then
    all_ok=0
    log "T6 MISMATCH: ${method} ${path} expected=${expect} actual=${actual}"
  fi
done

# Header injection — attacker tries to assert role via custom header. Server
# must ignore these and gate only on the JWT's realm_access.roles.
header_inj=$(curl -sS -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-User-Role: admin" \
  -H "X-Forwarded-User: admin" \
  -H "X-Original-User: admin" \
  -H "X-Roles: admin,user" \
  "$API_URL/admin/users")
{
  echo ""
  echo "## header injection attempt (X-User-Role: admin etc.)"
  echo "GET /admin/users → ${header_inj} (expect 403)"
} >>"$t6_file"
[ "$header_inj" != "403" ] && { all_ok=0; log "T6 MISMATCH: header injection got ${header_inj}, expected 403"; }

# Cross-client token — service account from saas-backend-admin should not be
# accepted by the API (its azp is not in KEYCLOAK_ALLOWED_CLIENT_IDS).
xclient_token=$(curl -sS -X POST \
  "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=$ADMIN_CLIENT_ID" -d "client_secret=$ADMIN_CLIENT_SECRET" \
  -d "grant_type=client_credentials" | jq -r '.access_token // empty')
if [ -n "$xclient_token" ] && [ "$xclient_token" != "null" ]; then
  xclient_code=$(curl -sS -o /dev/null -w "%{http_code}" \
    -H "Authorization: Bearer $xclient_token" "$API_URL/admin/users")
  {
    echo ""
    echo "## cross-client token (client_credentials grant, client_id=${ADMIN_CLIENT_ID})"
    echo "GET /admin/users → ${xclient_code} (expect 401 — client_id not in KEYCLOAK_ALLOWED_CLIENT_IDS)"
  } >>"$t6_file"
  [ "$xclient_code" != "401" ] && { all_ok=0; log "T6 MISMATCH: cross-client token got ${xclient_code}, expected 401"; }
else
  {
    echo ""
    echo "## cross-client token — could not acquire (skipped; client_credentials may be disabled for ${ADMIN_CLIENT_ID})"
  } >>"$t6_file"
fi

if [ "$all_ok" = "1" ]; then
  verdict T6 PASS "every admin verb + header injection + cross-client token denied"
else
  verdict T6 FAIL "one or more privilege-escalation probes were not denied (see ${t6_file})"
fi

# ─── Summary ────────────────────────────────────────────────────────────────
log ""
log "─────────────────────────────────────────────────────────────"
log "TOTAL: $((PASS_COUNT + FAIL_COUNT + INFO_COUNT))   PASS: ${PASS_COUNT}   FAIL: ${FAIL_COUNT}   INFO: ${INFO_COUNT}"
if [ "$FAIL_COUNT" -gt 0 ]; then
  log "FAILED: ${FAILED_IDS[*]}"
  log "Result: FAIL"
  exit 1
fi
log "Result: PASS (no FAILs; ${INFO_COUNT} informational findings recorded)"
exit 0
