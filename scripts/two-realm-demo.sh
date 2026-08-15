#!/usr/bin/env bash
# two-realm-demo.sh — stand up two workspaces pointing at two Keycloak realms,
# assert that they are isolated, and (with --keep) leave the whole thing running
# so the console can be driven by hand.
#
# It exists because the console's central claim — "two realms, one console, no
# leakage" — cannot be proven by unit tests: it needs two real realms with
# recognisably different data. Every call it makes is one the console makes.
#
# ─── Usage ──────────────────────────────────────────────────────────────────
#
#   # assertions only, tears everything down afterwards:
#   DB_URL=postgres://…  ./scripts/two-realm-demo.sh
#
#   # leave it running and open http://localhost:58080/admin :
#   DB_URL=postgres://…  ./scripts/two-realm-demo.sh --keep
#
# Environment:
#   DB_URL                    required; an EMPTY database (see the warning below)
#   KEYCLOAK_VERIFY_URL       default http://localhost:8081
#   KEYCLOAK_VERIFY_ADMIN     default admin
#   KEYCLOAK_VERIFY_PASSWORD  default admin
#   API_PORT                  default 58080
#
# ─── What it touches ────────────────────────────────────────────────────────
#
# Creates three DISPOSABLE realms and deletes them on exit (unless --keep). It
# never touches any pre-existing realm, container, volume or database:
#
#   lw-demo-control  — the INSTALLATION realm the operator authenticates against
#   lw-demo-a        — workspace A's realm (users: alice-alpha, bob-alpha)
#   lw-demo-b        — workspace B's realm (users: carol-bravo, dave-bravo)
#
# > **Point DB_URL at a throwaway database.** The script creates workspaces with
# > fixed slugs, so a second run against the same database fails on the slug's
# > unique index. It does NOT drop anything for you — deleting a database you
# > pointed it at by accident is not a decision a demo script should make.
#
# ─── What it proves ─────────────────────────────────────────────────────────
#
#   1. two workspaces resolve to two different realms
#   2. reads are isolated
#   3. a mutation in A is absent from B, and vice versa
#   4. a genuinely read-only connection is graded read_only / can_write=false
#      and its writes are refused with connection_read_only  (TD-024)
#   5. an archived workspace refuses identity operations
set -uo pipefail

KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

KC="${KEYCLOAK_VERIFY_URL:-http://localhost:8081}"
KC_ADMIN="${KEYCLOAK_VERIFY_ADMIN:-admin}"
KC_PASS="${KEYCLOAK_VERIFY_PASSWORD:-admin}"
API_PORT="${API_PORT:-58080}"
API="http://localhost:${API_PORT}"
DB="${DB_URL:?DB_URL required}"
# Scratch lives outside the repo so a run leaves no untracked files behind.
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/lw-two-realm-demo.XXXXXX")"
LOG="$WORKDIR/api.log"

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi; }

jqp() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

# ── Keycloak admin helpers ──────────────────────────────────────────────────
kct() { curl -s -X POST "$KC/realms/master/protocol/openid-connect/token" \
  -d "grant_type=password&client_id=admin-cli&username=$KC_ADMIN&password=$KC_PASS" | jqp "d['access_token']"; }
KCT="$(kct)"
[ -n "$KCT" ] || { echo "cannot authenticate to Keycloak at $KC"; exit 1; }

kc() { # method path [body]
  local m="$1" p="$2" b="${3:-}"
  if [ -n "$b" ]; then
    curl -s -o /dev/null -w '%{http_code}' -X "$m" "$KC$p" -H "Authorization: Bearer $KCT" -H 'Content-Type: application/json' -d "$b"
  else
    curl -s -o /dev/null -w '%{http_code}' -X "$m" "$KC$p" -H "Authorization: Bearer $KCT"
  fi
}
kcg() { curl -s "$KC$1" -H "Authorization: Bearer $KCT"; }

cleanup() {
  if [ "$KEEP" = "1" ]; then
    step "left running for the manual walkthrough (--keep)"
    cat <<EOF
  console      ${API}/admin
  operator     operator / operator-pw   (realm lw-demo-control)
  workspaces   Alpha → realm lw-demo-a  (alice-alpha, bob-alpha)
               Bravo → realm lw-demo-b  (carol-bravo, dave-bravo, erin-bravo)
               "Alpha read-only" → realm lw-demo-a via a view-only client (ARCHIVED by the run)
  api log      ${LOG}
  api pid      ${API_PID:-none}

  Tear it down with:
    kill ${API_PID:-<pid>}
    for r in lw-demo-control lw-demo-a lw-demo-b; do
      curl -s -X DELETE "${KC}/admin/realms/\$r" -H "Authorization: Bearer \\\$TOKEN"
    done
EOF
    return
  fi
  step "cleanup (only resources this script created)"
  [ -n "${API_PID:-}" ] && kill "$API_PID" 2>/dev/null && echo "  stopped api pid $API_PID"
  KCT="$(kct)"
  for r in lw-demo-control lw-demo-a lw-demo-b; do
    kc DELETE "/admin/realms/$r" >/dev/null && echo "  deleted realm $r"
  done
}
trap cleanup EXIT

make_realm() { kc DELETE "/admin/realms/$1" >/dev/null; kc POST /admin/realms "{\"realm\":\"$1\",\"enabled\":true}" >/dev/null; }

make_user() { # realm username [password]
  # firstName/lastName and an empty requiredActions matter: without them
  # Keycloak's VERIFY_PROFILE action fires and the password grant answers
  # "Account is not fully set up".
  kc POST "/admin/realms/$1/users" \
    "{\"username\":\"$2\",\"email\":\"$2@example.test\",\"firstName\":\"$2\",\"lastName\":\"Test\",\"enabled\":true,\"emailVerified\":true,\"requiredActions\":[]}" >/dev/null
  if [ -n "${3:-}" ]; then
    local uid; uid="$(kcg "/admin/realms/$1/users?username=$2" | jqp "d[0]['id']")"
    kc PUT "/admin/realms/$1/users/$uid/reset-password" \
      "{\"type\":\"password\",\"value\":\"$3\",\"temporary\":false}" >/dev/null
    echo "$uid"
  fi
}

make_sa_client() { # realm clientId role...  → prints secret
  local realm="$1" cid="$2"; shift 2
  kc POST "/admin/realms/$realm/clients" \
    "{\"clientId\":\"$cid\",\"enabled\":true,\"publicClient\":false,\"serviceAccountsEnabled\":true,\"standardFlowEnabled\":false,\"directAccessGrantsEnabled\":false}" >/dev/null
  local iid; iid="$(kcg "/admin/realms/$realm/clients?clientId=$cid" | jqp "d[0]['id']")"
  local secret; secret="$(kcg "/admin/realms/$realm/clients/$iid/client-secret" | jqp "d['value']")"
  if [ $# -gt 0 ]; then
    local su; su="$(kcg "/admin/realms/$realm/clients/$iid/service-account-user" | jqp "d['id']")"
    local mgmt; mgmt="$(kcg "/admin/realms/$realm/clients?clientId=realm-management" | jqp "d[0]['id']")"
    local roles; roles="$(kcg "/admin/realms/$realm/clients/$mgmt/roles")"
    local want; want="$(printf '%s\n' "$@" | paste -sd, -)"
    local payload; payload="$(printf '%s' "$roles" | python3 -c "
import sys,json
want=set('$want'.split(','))
print(json.dumps([r for r in json.load(sys.stdin) if r['name'] in want]))")"
    kc POST "/admin/realms/$realm/users/$su/role-mappings/clients/$mgmt" "$payload" >/dev/null
  fi
  echo "$secret"
}

# ── 1. Build the three realms ───────────────────────────────────────────────
step "building disposable realms"
make_realm lw-demo-control
make_realm lw-demo-a
make_realm lw-demo-b

# Control realm: an operator with the `admin` realm role + a public client for
# the password grant, plus a confidential client the API validates against.
kc POST /admin/realms/lw-demo-control/roles '{"name":"admin"}' >/dev/null
kc POST /admin/realms/lw-demo-control/clients \
  '{"clientId":"lw-console","enabled":true,"publicClient":true,"standardFlowEnabled":true,"directAccessGrantsEnabled":true}' >/dev/null
OP_ID="$(make_user lw-demo-control operator "operator-pw")"
ADMIN_ROLE="$(kcg /admin/realms/lw-demo-control/roles/admin)"
kc POST "/admin/realms/lw-demo-control/users/$OP_ID/role-mappings/realm" "[$ADMIN_ROLE]" >/dev/null
CTRL_SECRET="$(make_sa_client lw-demo-control lw-api-admin realm-admin)"

# Workspace realms, with DISTINCT recognizable users.
make_user lw-demo-a alice-alpha
make_user lw-demo-a bob-alpha
make_user lw-demo-b carol-bravo
make_user lw-demo-b dave-bravo
A_SECRET="$(make_sa_client lw-demo-a lw-conn realm-admin)"
B_SECRET="$(make_sa_client lw-demo-b lw-conn realm-admin)"
# A second, deliberately READ-ONLY administrative client in realm A.
A_RO_SECRET="$(make_sa_client lw-demo-a lw-conn-readonly view-realm view-users)"
echo "  realms + clients ready"

# ── 2. Boot the API ─────────────────────────────────────────────────────────
step "booting the API against the ephemeral database"
export DB_URL="$DB" \
  DB_MIGRATE_ON_BOOT=true \
  PORT="$API_PORT" \
  KEYCLOAK_URL="$KC" \
  KEYCLOAK_REALM=lw-demo-control \
  KEYCLOAK_CLIENT_ID=lw-console \
  KEYCLOAK_CLIENT_SECRET=unused \
  KEYCLOAK_JWKS_URL="$KC/realms/lw-demo-control/protocol/openid-connect/certs" \
  KEYCLOAK_ALLOWED_CLIENT_IDS=lw-console \
  KEYCLOAK_ADMIN_CLIENT_ID=lw-api-admin \
  KEYCLOAK_ADMIN_CLIENT_SECRET="$CTRL_SECRET" \
  SECRETS_MASTER_KEY="$(python3 -c 'import base64;print(base64.b64encode(b"slice6-acceptance-master-key-32b").decode())')" \
  ADMIN_CONSOLE_ENABLED=true \
  DEV_PLAYGROUND_ENABLED=false \
  GIN_ACCESS_LOG_ENABLED=false

./bin/api > "$LOG" 2>&1 &
API_PID=$!
for i in $(seq 1 40); do
  curl -fsS -o /dev/null "$API/health" 2>/dev/null && { echo "  api up after ${i}s (pid $API_PID)"; break; }
  sleep 1
done
curl -fsS -o /dev/null "$API/health" || { echo "  api failed to start:"; tail -30 "$LOG"; exit 1; }

TOKEN="$(curl -s -X POST "$KC/realms/lw-demo-control/protocol/openid-connect/token" \
  -d "grant_type=password&client_id=lw-console&username=operator&password=operator-pw" | jqp "d['access_token']")"
[ -n "$TOKEN" ] || { echo "  operator token failed"; exit 1; }
echo "  operator authenticated against lw-demo-control"

# v1 prints the body on stdout and writes the status to a FILE.
#
# A shell variable would not survive: every call site is `X="$(v1 …)"`, which
# runs the function in a subshell, so an assignment inside it is discarded and
# the caller would read the status of some earlier request. That is exactly the
# bug that made a correctly-refused 409 look like a 200 on the first run.
STATUS_FILE="$WORKDIR/.v1_status"

# pace keeps this script under the /v1 edge rate limit.
#
# That limiter is per IP, sits BEFORE authentication, and is tuned for a human
# admin's click-rate (10 req/s, burst 20). This script fires several hundred
# requests from one address as fast as curl can start, so without pacing it
# spends most of its run reading its own 429s. The limit is the product's, not a
# test artefact, so the harness slows down rather than the limit going up.
#
# 0.15s ≈ 6.6 req/s, comfortably under the refill rate.
pace() { sleep 0.15; }

v1() { # method path [body]
  pace
  local m="$1" p="$2" b="${3:-}" out
  if [ -n "$b" ]; then
    out="$(curl -s -w '\n%{http_code}' -X "$m" "$API$p" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$b")"
  else
    out="$(curl -s -w '\n%{http_code}' -X "$m" "$API$p" -H "Authorization: Bearer $TOKEN")"
  fi
  printf '%s' "$out" | tail -1 > "$STATUS_FILE"
  printf '%s' "$out" | sed '$d'
}
status() { cat "$STATUS_FILE"; }

# ── 3. Create both workspaces and connect them ──────────────────────────────
step "creating two workspaces, each connected to its own realm"
WS_A="$(v1 POST /v1/workspaces '{"name":"Alpha"}' | jqp "d['id']")"
WS_B="$(v1 POST /v1/workspaces '{"name":"Bravo"}' | jqp "d['id']")"
echo "  A=$WS_A  B=$WS_B"

connect() { # workspace realm secret clientid → prints connection id
  local wsid="$1" realm="$2" secret="$3" cid="${4:-lw-conn}"
  v1 POST "/v1/workspaces/$wsid/connections" \
    "{\"name\":\"$realm\",\"base_url\":\"$KC\",\"realm\":\"$realm\",\"client_id\":\"$cid\",\"client_secret\":\"$secret\"}" | jqp "d['id']"
}

CONN_A="$(connect "$WS_A" lw-demo-a "$A_SECRET")"
CONN_B="$(connect "$WS_B" lw-demo-b "$B_SECRET")"

REPORT_A="$(v1 POST "/v1/workspaces/$WS_A/connections/$CONN_A/verify")"
check "workspace A connection verifies as full"      "$(printf '%s' "$REPORT_A" | jqp "d['report']['access_mode']")" "full"
check "workspace A connection reports can_write"     "$(printf '%s' "$REPORT_A" | jqp "str(d['connection']['can_write']).lower()")" "true"
v1 POST "/v1/workspaces/$WS_B/connections/$CONN_B/verify" >/dev/null

v1 POST "/v1/workspaces/$WS_A/connections/$CONN_A/activate" >/dev/null
check "activate A" "$(status)" "200"
v1 POST "/v1/workspaces/$WS_B/connections/$CONN_B/activate" >/dev/null
check "activate B" "$(status)" "200"

# ── 4. Reads are isolated ───────────────────────────────────────────────────
step "reads are isolated between the two realms"
USERS_A="$(v1 GET "/v1/workspaces/$WS_A/users" | jqp "','.join(sorted(u['username'] for u in d['users']))")"
USERS_B="$(v1 GET "/v1/workspaces/$WS_B/users" | jqp "','.join(sorted(u['username'] for u in d['users']))")"
echo "  A users: $USERS_A"
echo "  B users: $USERS_B"
check "workspace A shows only realm A's users" "$USERS_A" "alice-alpha,bob-alpha"
check "workspace B shows only realm B's users" "$USERS_B" "carol-bravo,dave-bravo"

# ── 5. Mutations are isolated ───────────────────────────────────────────────
step "mutations are isolated"
v1 POST "/v1/workspaces/$WS_A/roles" '{"name":"alpha-only-role","description":"created in A"}' >/dev/null
check "create role in A" "$(status)" "201"

ROLES_B="$(v1 GET "/v1/workspaces/$WS_B/roles" | jqp "','.join(r['name'] for r in d['roles'])")"
case "$ROLES_B" in
  *alpha-only-role*) bad "a role created in A leaked into B" ;;
  *)                 ok  "the role created in A is absent from B" ;;
esac

v1 POST "/v1/workspaces/$WS_B/users" \
  '{"email":"erin-bravo@example.test","first_name":"Erin","last_name":"Bravo","temporary_password":"temp-pw-12345","roles":[]}' >/dev/null
check "create user in B" "$(status)" "201"

USERS_A2="$(v1 GET "/v1/workspaces/$WS_A/users" | jqp "','.join(sorted(u['username'] for u in d['users']))")"
check "workspace A is unchanged by B's mutation" "$USERS_A2" "alice-alpha,bob-alpha"

USERS_B2="$(v1 GET "/v1/workspaces/$WS_B/users" | jqp "','.join(sorted(u['username'] for u in d['users']))")"
check "workspace B contains only its own change" "$USERS_B2" "carol-bravo,dave-bravo,erin-bravo@example.test"

# ── 6. TD-024: a read-only connection is graded honestly and refuses writes ──
step "TD-024: a read-only administrative client"
WS_RO="$(v1 POST /v1/workspaces '{"name":"Alpha read-only"}' | jqp "d['id']")"
CONN_RO="$(connect "$WS_RO" lw-demo-a "$A_RO_SECRET" lw-conn-readonly)"
REPORT_RO="$(v1 POST "/v1/workspaces/$WS_RO/connections/$CONN_RO/verify")"

check "read-only client is NOT graded full"   "$(printf '%s' "$REPORT_RO" | jqp "str(d['report']['access_mode']=='full').lower()")" "false"
check "read-only client is graded read_only"  "$(printf '%s' "$REPORT_RO" | jqp "d['report']['access_mode']")" "read_only"
check "read-only client reports can_write=false" "$(printf '%s' "$REPORT_RO" | jqp "str(d['connection']['can_write']).lower()")" "false"
check "the connection is still healthy"       "$(printf '%s' "$REPORT_RO" | jqp "d['connection']['health']")" "healthy"

v1 POST "/v1/workspaces/$WS_RO/connections/$CONN_RO/activate" >/dev/null
RO_USERS="$(v1 GET "/v1/workspaces/$WS_RO/users" | jqp "len(d['users'])")"
check "reads still work through a read-only connection" "$RO_USERS" "2"

RO_WRITE="$(v1 POST "/v1/workspaces/$WS_RO/roles" '{"name":"should-not-exist"}')"
check "a write through it is refused"            "$(status)" "409"
check "…with the connection_read_only code"      "$(printf '%s' "$RO_WRITE" | jqp "d['error']['code']")" "connection_read_only"
check "…and the error carries a request id"      "$(printf '%s' "$RO_WRITE" | jqp "str(len(d['error']['request_id'])>0).lower()")" "true"

ROLES_A_AFTER="$(v1 GET "/v1/workspaces/$WS_A/roles" | jqp "','.join(r['name'] for r in d['roles'])")"
case "$ROLES_A_AFTER" in
  *should-not-exist*) bad "the refused write reached the realm anyway" ;;
  *)                  ok  "the refused write left the realm untouched" ;;
esac

# ── 6b. Project credentials: the machine-to-machine path ────────────────────
#
# Everything below drives the SAME /v1 endpoints an external backend would,
# using an opaque project credential instead of an operator token. It is the
# only place the full path is exercised end to end against real Keycloak:
#
#   external backend → credential → workspace binding → connection → realm
step "project credentials (machine-to-machine)"

PRJ_A="$(v1 POST "/v1/workspaces/$WS_A/projects" '{"name":"Billing worker"}' | jqp "d['id']")"
check "create project in A" "$(status)" "201"
echo "  project A = $PRJ_A"

# Two credentials for the same project, with different power. This is what
# makes rotation and least privilege expressible without a rotate endpoint.
CRED_READ_JSON="$(v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials" \
  '{"label":"read-only key","scopes":["users:read"]}')"
check "create read-only credential" "$(status)" "201"
KEY_READ="$(printf '%s' "$CRED_READ_JSON" | jqp "d['secret']")"
KEY_READ_ID="$(printf '%s' "$CRED_READ_JSON" | jqp "d['credential']['id']")"

CRED_RW_JSON="$(v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials" \
  '{"label":"read-write key","scopes":["users:read","users:write","roles:write"]}')"
check "create read-write credential" "$(status)" "201"
KEY_RW="$(printf '%s' "$CRED_RW_JSON" | jqp "d['secret']")"

# The secret is never printed in full by this script, only its public prefix.
echo "  read-only key  = $(printf '%s' "$KEY_READ" | cut -c1-22)…REDACTED"
echo "  read-write key = $(printf '%s' "$KEY_RW"   | cut -c1-22)…REDACTED"

check "the credential token carries the lw_sk_ prefix" \
  "$(printf '%s' "$KEY_READ" | cut -c1-6)" "lw_sk_"

# prj sends a request AS THE PROJECT: same header, opaque credential.
STATUS_FILE_PRJ="$WORKDIR/.prj_status"
prj() { # key method path [body]
  pace
  local key="$1" m="$2" p="$3" b="${4:-}" out
  if [ -n "$b" ]; then
    out="$(curl -s -w '\n%{http_code}' -X "$m" "$API$p" -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -d "$b")"
  else
    out="$(curl -s -w '\n%{http_code}' -X "$m" "$API$p" -H "Authorization: Bearer $key")"
  fi
  printf '%s' "$out" | tail -1 > "$STATUS_FILE_PRJ"
  printf '%s' "$out" | sed '$d'
}
pstatus() { cat "$STATUS_FILE_PRJ"; }

# ── correct workspace ──
PRJ_USERS="$(prj "$KEY_READ" GET "/v1/workspaces/$WS_A/users" | jqp "','.join(sorted(u['username'] for u in d['users']))")"
check "credential reads its OWN workspace" "$(pstatus)" "200"
check "…and sees exactly realm A's users"  "$PRJ_USERS" "alice-alpha,bob-alpha"

# ── wrong workspace ──
MISMATCH="$(prj "$KEY_READ" GET "/v1/workspaces/$WS_B/users")"
check "credential is refused on workspace B"        "$(pstatus)" "403"
check "…with workspace_mismatch"                    "$(printf '%s' "$MISMATCH" | jqp "d['error']['code']")" "workspace_mismatch"

# ── nonexistent workspace is indistinguishable ──
GHOST="$(prj "$KEY_READ" GET "/v1/workspaces/ws_00000000-0000-4000-8000-000000000000/users")"
check "a nonexistent workspace answers identically" "$(pstatus)" "403"
check "…with the same code"                         "$(printf '%s' "$GHOST" | jqp "d['error']['code']")" "workspace_mismatch"

# ── scope enforcement ──
NOSCOPE="$(prj "$KEY_READ" POST "/v1/workspaces/$WS_A/invitations" '{"email":"x@example.test","roles":[]}')"
check "read-only key cannot write"      "$(pstatus)" "403"
check "…with insufficient_scope"        "$(printf '%s' "$NOSCOPE" | jqp "d['error']['code']")" "insufficient_scope"

# ── a real write, reaching the correct realm ──
prj "$KEY_RW" POST "/v1/workspaces/$WS_A/users" \
  '{"email":"frank-alpha@example.test","first_name":"Frank","last_name":"Alpha","temporary_password":"temp-pw-12345","roles":[]}' >/dev/null
check "read-write key creates a user" "$(pstatus)" "201"

A_AFTER="$(v1 GET "/v1/workspaces/$WS_A/users" | jqp "','.join(sorted(u['username'] for u in d['users']))")"
case "$A_AFTER" in
  *frank-alpha*) ok "the project's write landed in realm A" ;;
  *)             bad "the project's write did not reach realm A ($A_AFTER)" ;;
esac
B_AFTER="$(v1 GET "/v1/workspaces/$WS_B/users" | jqp "','.join(sorted(u['username'] for u in d['users']))")"
case "$B_AFTER" in
  *frank-alpha*) bad "a project write in A leaked into realm B" ;;
  *)             ok  "realm B is untouched by the project's write" ;;
esac

# ── role privilege protection ──
FRANK="$(v1 GET "/v1/workspaces/$WS_A/users?search=frank-alpha" | jqp "d['users'][0]['id']")"
ESCALATE="$(prj "$KEY_RW" POST "/v1/workspaces/$WS_A/users/$FRANK/roles" '{"roles":["admin"]}')"
check "a project cannot grant the admin role"  "$(pstatus)" "403"
check "…with role_privileged"                  "$(printf '%s' "$ESCALATE" | jqp "d['error']['code']")" "role_privileged"

prj "$KEY_RW" POST "/v1/workspaces/$WS_A/roles" '{"name":"project-made-role"}' >/dev/null
check "…but ordinary roles are still creatable" "$(pstatus)" "201"

# ── control plane is unreachable ──
CP="$(prj "$KEY_RW" GET "/v1/workspaces")"
check "a credential cannot list workspaces"     "$(pstatus)" "403"
check "…with operator_only"                     "$(printf '%s' "$CP" | jqp "d['error']['code']")" "operator_only"

CP2="$(prj "$KEY_RW" GET "/v1/workspaces/$WS_A/connections")"
check "a credential cannot read connections"    "$(pstatus)" "403"
check "…with operator_only"                     "$(printf '%s' "$CP2" | jqp "d['error']['code']")" "operator_only"

CP3="$(prj "$KEY_RW" POST "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials" '{"label":"self-issued","scopes":["users:read"]}')"
check "a credential cannot mint credentials"    "$(pstatus)" "403"
check "…with operator_only"                     "$(printf '%s' "$CP3" | jqp "d['error']['code']")" "operator_only"

PW="$(prj "$KEY_RW" PUT "/v1/workspaces/$WS_A/users/$FRANK/password" '{"password":"hijacked-123456"}')"
check "a credential cannot set a password directly" "$(pstatus)" "403"
check "…with operator_only"                         "$(printf '%s' "$PW" | jqp "d['error']['code']")" "operator_only"

# ── legacy surface never accepts a credential ──
prj "$KEY_RW" GET "/admin/users" >/dev/null
check "/admin/* refuses a project credential"   "$(pstatus)" "401"

# ── list never returns the secret ──
CRED_LIST="$(v1 GET "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials")"
check "credential listing carries no secret" \
  "$(printf '%s' "$CRED_LIST" | jqp "str(any('secret' in k for c in d['credentials'] for k in c)).lower()")" "false"
check "…but does carry the key prefix" \
  "$(printf '%s' "$CRED_LIST" | jqp "str(all(c['key_prefix'] for c in d['credentials'])).lower()")" "true"

# ── revocation is immediate ──
v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials/$KEY_READ_ID/revoke" >/dev/null
check "revoke the read-only credential" "$(status)" "200"

REVOKED="$(prj "$KEY_READ" GET "/v1/workspaces/$WS_A/users")"
check "the revoked credential fails on the NEXT request" "$(pstatus)" "401"
check "…with credential_invalid"                          "$(printf '%s' "$REVOKED" | jqp "d['error']['code']")" "credential_invalid"

prj "$KEY_RW" GET "/v1/workspaces/$WS_A/users" >/dev/null
check "the other credential is unaffected" "$(pstatus)" "200"

# ── authentication failures are indistinguishable ──
BOGUS="lw_sk_aaaaaaaaaaaaaaaa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
UNKNOWN="$(prj "$BOGUS" GET "/v1/workspaces/$WS_A/users")"
check "an unknown credential answers 401" "$(pstatus)" "401"
UNKNOWN_CODE="$(printf '%s' "$UNKNOWN" | jqp "d['error']['code']")"
REVOKED_CODE="$(printf '%s' "$REVOKED" | jqp "d['error']['code']")"
check "…with the same code as a revoked one" "$UNKNOWN_CODE" "$REVOKED_CODE"

# ── archiving the project stops everything ──
v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_A/archive" >/dev/null
check "archive the project" "$(status)" "200"

prj "$KEY_RW" GET "/v1/workspaces/$WS_A/users" >/dev/null
check "archiving stops the remaining credential immediately" "$(pstatus)" "401"

# ── the secret never appears in the audit trail ──
AUDIT="$(v1 GET /admin/audit-events)"
case "$AUDIT" in
  *"$KEY_RW"*|*"$KEY_READ"*) bad "a credential secret appears in the audit trail" ;;
  *)                          ok  "no credential secret appears in the audit trail" ;;
esac
case "$AUDIT" in
  *"$PRJ_A"*) ok  "audit events attribute actions to the project id" ;;
  *)          bad "no project attribution found in the audit trail" ;;
esac

# ── 7. Archived workspace refuses identity operations ───────────────────────
step "an archived workspace is a real boundary"
v1 POST "/v1/workspaces/$WS_RO/archive" >/dev/null
ARCH="$(v1 GET "/v1/workspaces/$WS_RO/users")"
check "archived workspace refuses reads"    "$(status)" "409"
check "…with the workspace_archived code"   "$(printf '%s' "$ARCH" | jqp "d['error']['code']")" "workspace_archived"

# ── 8. The console's own boot calls ─────────────────────────────────────────
step "the calls the console makes at boot"
# The default list is ACTIVE ONLY (workspace.ParseStatusFilter), so the
# archived one is correctly absent here — that is what the selector consumes.
LIST="$(v1 GET /v1/workspaces)"
check "GET /v1/workspaces lists the two ACTIVE workspaces"  "$(printf '%s' "$LIST" | jqp "d['count']")" "2"
LIST_ALL="$(v1 GET '/v1/workspaces?status=all')"
check "…and ?status=all still shows the archived one"        "$(printf '%s' "$LIST_ALL" | jqp "d['count']")" "3"
ACTIVE_A="$(v1 GET "/v1/workspaces/$WS_A/connections?status=active")"
check "the active-connection filter returns exactly one" "$(printf '%s' "$ACTIVE_A" | jqp "d['count']")" "1"
check "…and it carries no secret" "$(printf '%s' "$ACTIVE_A" | jqp "str(any('secret' in k and k!='has_client_secret' for k in d['connections'][0])).lower()")" "false"
check "…and reports has_client_secret" "$(printf '%s' "$ACTIVE_A" | jqp "str(d['connections'][0]['has_client_secret']).lower()")" "true"

printf '\n\033[1m== result: %d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
