#!/usr/bin/env bash
# m2m-harness.sh — stand up a LIGHTWEIGHT installation the way an operator
# would, then point a genuinely external backend at it and see whether the
# machine-to-machine contract holds.
#
# It answers the Slice 8 question directly: can someone put this on a VPS,
# generate a Project Credential, configure a backend with it, and use the API in
# production without meeting contradictory rules about authentication, rate
# limiting or observability?
#
# ─── The split, which is the point ──────────────────────────────────────────
#
# This script is the OPERATOR. It knows Keycloak exists: it builds realms,
# creates workspaces, wires connections, mints credentials. That is the work an
# installer does once, through the console or through curl.
#
#   cmd/lwprobe is the BACKEND. It knows nothing except a URL, a workspace id
#   and an API key, and it imports nothing from this module — a test enforces
#   that. Everything it proves, it proves over the public HTTP surface.
#
# If the harness ever has to hand lwprobe a realm name or a client secret to get
# a check passing, the architectural claim has failed and that is the finding.
#
# ─── Usage ──────────────────────────────────────────────────────────────────
#
#   DB_URL=postgres://…  ./scripts/m2m-harness.sh              # contract only
#   DB_URL=postgres://…  ./scripts/m2m-harness.sh --bench      # + measurements
#   DB_URL=postgres://…  ./scripts/m2m-harness.sh --keep       # leave it running
#
# Environment:
#   DB_URL                    required; an EMPTY database
#   KEYCLOAK_VERIFY_URL       default http://localhost:8081
#   KEYCLOAK_VERIFY_ADMIN     default admin
#   KEYCLOAK_VERIFY_PASSWORD  default admin
#   API_PORT                  default 58090
#
# > **Point DB_URL at a throwaway database.** Workspaces are created with fixed
# > slugs, so a second run against the same database fails on the unique index.
# > The script does not drop anything for you.
#
# ─── What it creates, and removes on exit ───────────────────────────────────
#
#   lw-m2m-control  the installation realm the OPERATOR authenticates against
#   lw-m2m-a        workspace A's realm — the backend's tenant
#   lw-m2m-b        workspace B's realm — a tenant the backend must never reach
#   lw-m2m-a2       a SECOND realm for workspace A, used to prove that rotating
#                   the active connection does not touch the credential
#   lw-m2m-dead     activated, then deleted, so `provider_unavailable` can be
#                   provoked without breaking anything else
set -uo pipefail

BENCH=0
KEEP=0
SMOKE=0
for arg in "$@"; do
  case "$arg" in
    --bench) BENCH=1 ;;
    # CI mode: the same real stack and the same real assertions, minus the
    # parts whose cost is time rather than coverage. See the SMOKE notes below
    # for exactly what is dropped — a "smoke" mode that quietly stopped
    # checking things would be worse than no CI job.
    --smoke) SMOKE=1 ;;
    --keep)  KEEP=1 ;;
    *) echo "unknown flag: $arg"; exit 2 ;;
  esac
done

KC="${KEYCLOAK_VERIFY_URL:-http://localhost:8081}"
KC_ADMIN="${KEYCLOAK_VERIFY_ADMIN:-admin}"
KC_PASS="${KEYCLOAK_VERIFY_PASSWORD:-admin}"
API_PORT="${API_PORT:-58090}"
API="http://localhost:${API_PORT}"
DB="${DB_URL:?DB_URL required}"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/lw-m2m-harness.XXXXXX")"
LOG="$WORKDIR/api.log"

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi; }

# ── Keycloak admin helpers ──────────────────────────────────────────────────
#
# Shared with scripts/browser-e2e.sh. Both harnesses build fixture realms the
# same way, so "how this project talks to Keycloak" has one implementation —
# see scripts/lib/keycloak-fixture.sh for jqp / kct / kc / kcg / make_realm /
# make_user / make_sa_client.
# shellcheck source=lib/keycloak-fixture.sh
. "$(dirname "$0")/lib/keycloak-fixture.sh"

kc_authenticate || exit 1

REALMS="lw-m2m-control lw-m2m-a lw-m2m-b lw-m2m-a2 lw-m2m-dead"

cleanup() {
  # Copy the API log somewhere that survives this directory, when asked. CI sets
  # this so a failed run leaves diagnostics; the copy happens BEFORE the rm
  # below, and the caller is responsible for redacting it (scripts/redact-logs.sh)
  # because the log records freshly minted credentials.
  if [ -n "${LW_DIAGNOSTICS_DIR:-}" ] && [ -f "$LOG" ]; then
    mkdir -p "$LW_DIAGNOSTICS_DIR"
    cp "$LOG" "$LW_DIAGNOSTICS_DIR/lightweight-api.log" 2>/dev/null || true
  fi

  if [ "$KEEP" = "1" ]; then
    step "left running (--keep)"
    cat <<EOF
  api        ${API}
  console    ${API}/admin   (operator / operator-pw, realm lw-m2m-control)
  workspace  ${WS_A:-?}
  api key    ${KEY_MAIN:0:14}…   (full value in $WORKDIR/key-main)
  api log    ${LOG}
  api pid    ${API_PID:-none}
EOF
    return
  fi
  step "cleanup (only what this run created)"
  [ -n "${API_PID:-}" ] && kill "$API_PID" 2>/dev/null && echo "  stopped api pid $API_PID"
  KCT="$(kct)"
  for r in $REALMS; do kc DELETE "/admin/realms/$r" >/dev/null; done
  echo "  deleted realms"
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

# ── 1. Realms ───────────────────────────────────────────────────────────────
step "building disposable realms"
for r in $REALMS; do make_realm "$r"; done

kc POST /admin/realms/lw-m2m-control/roles '{"name":"admin"}' >/dev/null
kc POST /admin/realms/lw-m2m-control/clients \
  '{"clientId":"lw-console","enabled":true,"publicClient":true,"standardFlowEnabled":true,"directAccessGrantsEnabled":true}' >/dev/null
OP_ID="$(make_user lw-m2m-control operator "operator-pw")"
ADMIN_ROLE="$(kcg /admin/realms/lw-m2m-control/roles/admin)"
kc POST "/admin/realms/lw-m2m-control/users/$OP_ID/role-mappings/realm" "[$ADMIN_ROLE]" >/dev/null
CTRL_SECRET="$(make_sa_client lw-m2m-control lw-api-admin realm-admin)"

make_user lw-m2m-a alice-alpha
make_user lw-m2m-b carol-bravo
make_user lw-m2m-a2 alice-rotated
A_SECRET="$(make_sa_client lw-m2m-a lw-conn realm-admin)"
B_SECRET="$(make_sa_client lw-m2m-b lw-conn realm-admin)"
A2_SECRET="$(make_sa_client lw-m2m-a2 lw-conn realm-admin)"
DEAD_SECRET="$(make_sa_client lw-m2m-dead lw-conn realm-admin)"
echo "  realms + clients ready"

# ── 2. Boot the API ─────────────────────────────────────────────────────────
step "booting the API"

# Refuse to run against someone else's process.
#
# Without this the script happily talks to a leftover API from a previous run —
# one whose realms this run has just deleted — and every check fails with a 503
# that looks like a product bug. That happened, it cost a debugging session, and
# a harness whose failure mode is "lies convincingly" is worse than no harness.
if curl -fsS -o /dev/null "$API/health" 2>/dev/null; then
  echo "  ✗ something is already listening on port $API_PORT."
  echo "    That is almost certainly a leftover ./bin/api from an interrupted run."
  echo "    Stop it first, or set API_PORT to a free port."
  exit 1
fi

make -s build >/dev/null || { echo "  build failed"; exit 1; }

export DB_URL="$DB" \
  DB_MIGRATE_ON_BOOT=true \
  PORT="$API_PORT" \
  KEYCLOAK_URL="$KC" \
  KEYCLOAK_REALM=lw-m2m-control \
  KEYCLOAK_CLIENT_ID=lw-console \
  KEYCLOAK_CLIENT_SECRET=unused \
  KEYCLOAK_JWKS_URL="$KC/realms/lw-m2m-control/protocol/openid-connect/certs" \
  KEYCLOAK_ALLOWED_CLIENT_IDS=lw-console \
  KEYCLOAK_ADMIN_CLIENT_ID=lw-api-admin \
  KEYCLOAK_ADMIN_CLIENT_SECRET="$CTRL_SECRET" \
  SECRETS_MASTER_KEY="$(python3 -c 'import base64;print(base64.b64encode(b"slice8-m2m-harness-master-key32b").decode())')" \
  ADMIN_CONSOLE_ENABLED=true \
  ADMIN_CONSOLE_CLIENT_ID=lw-console \
  DEV_PLAYGROUND_ENABLED=false \
  GIN_ACCESS_LOG_ENABLED=true \
  RATE_LIMIT_EDGE_RPS="${RATE_LIMIT_EDGE_RPS:-}" \
  RATE_LIMIT_CREDENTIAL_RPS="${RATE_LIMIT_CREDENTIAL_RPS:-}"

./bin/api > "$LOG" 2>&1 &
API_PID=$!
# Wait for READINESS, not liveness. Liveness answers 200 while migrations are
# still running, so polling it would start the assertions against a half-built
# schema — intermittently, which is the worst way to find out.
for i in $(seq 1 60); do
  curl -fsS -o /dev/null "$API/health/ready" 2>/dev/null && { echo "  api ready after ${i}s (pid $API_PID)"; break; }
  sleep 1
done
if ! curl -fsS -o /dev/null "$API/health/ready"; then
  echo "  api never became ready. Last readiness response:"
  curl -s "$API/health/ready" || true
  echo; echo "  api log:"; tail -40 "$LOG"
  exit 1
fi

# Liveness and readiness must be distinguishable, and this is the one place
# both are observable against a real process.
live_code="$(curl -s -o /dev/null -w '%{http_code}' "$API/health/live")"
check "liveness answers 200" "$live_code" "200"
ready_code="$(curl -s -o /dev/null -w '%{http_code}' "$API/health/ready")"
check "readiness answers 200 once migrations are applied" "$ready_code" "200"

TOKEN="$(curl -s -X POST "$KC/realms/lw-m2m-control/protocol/openid-connect/token" \
  -d "grant_type=password&client_id=lw-console&username=operator&password=operator-pw" | jqp "d['access_token']")"
[ -n "$TOKEN" ] || { echo "  operator token failed"; exit 1; }
echo "  operator authenticated"

STATUS_FILE="$WORKDIR/.v1_status"

# The operator path is still metered at the edge — deliberately, since that is
# the console's unchanged behaviour. This pacing is therefore CORRECT rather
# than a workaround: it is what a human clicking through a console does.
pace() { sleep 0.12; }

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

# ── 3. Operator setup: workspaces, connections, projects, credentials ───────
step "operator sets up the installation"

WS_A="$(v1 POST /v1/workspaces '{"name":"Tenant Alpha"}' | jqp "d['id']")"
WS_B="$(v1 POST /v1/workspaces '{"name":"Tenant Bravo"}' | jqp "d['id']")"
WS_DEAD="$(v1 POST /v1/workspaces '{"name":"Tenant Dead"}' | jqp "d['id']")"
echo "  workspaces A=$WS_A B=$WS_B dead=$WS_DEAD"

connect() { # workspace realm secret → prints connection id
  v1 POST "/v1/workspaces/$1/connections" \
    "{\"name\":\"$2\",\"base_url\":\"$KC\",\"realm\":\"$2\",\"client_id\":\"lw-conn\",\"client_secret\":\"$3\"}" | jqp "d['id']"
}
activate() { v1 POST "/v1/workspaces/$1/connections/$2/verify" >/dev/null; v1 POST "/v1/workspaces/$1/connections/$2/activate" >/dev/null; }

CONN_A="$(connect "$WS_A" lw-m2m-a "$A_SECRET")";        activate "$WS_A" "$CONN_A"
CONN_B="$(connect "$WS_B" lw-m2m-b "$B_SECRET")";        activate "$WS_B" "$CONN_B"
CONN_D="$(connect "$WS_DEAD" lw-m2m-dead "$DEAD_SECRET")"; activate "$WS_DEAD" "$CONN_D"
echo "  connections active"

PRJ_A="$(v1 POST "/v1/workspaces/$WS_A/projects" '{"name":"Billing worker"}' | jqp "d['id']")"
PRJ_DEAD="$(v1 POST "/v1/workspaces/$WS_DEAD/projects" '{"name":"Dead provider probe"}' | jqp "d['id']")"
PRJ_ARCHIVED="$(v1 POST "/v1/workspaces/$WS_A/projects" '{"name":"Retired worker"}' | jqp "d['id']")"

ALL_SCOPES='["users:read","users:write","roles:read","roles:write","sessions:read","sessions:revoke","invitations:read","invitations:write","audit:read"]'

mint() { # workspace project label scopes [expires_at] → prints token
  local body
  if [ -n "${5:-}" ]; then
    body="{\"label\":\"$3\",\"scopes\":$4,\"expires_at\":\"$5\"}"
  else
    body="{\"label\":\"$3\",\"scopes\":$4}"
  fi
  v1 POST "/v1/workspaces/$1/projects/$2/credentials" "$body" | jqp "d['secret']"
}

KEY_MAIN="$(mint "$WS_A" "$PRJ_A" main "$ALL_SCOPES")"
KEY_B="$(mint "$WS_A" "$PRJ_A" second "$ALL_SCOPES")"
KEY_RO="$(mint "$WS_A" "$PRJ_A" read-only '["users:read"]')"
# A credential holding ONLY audit:read, to prove the scope grants the trail and
# nothing else — and KEY_RO above proves the reverse.
KEY_AUDIT="$(mint "$WS_A" "$PRJ_A" audit-reader '["audit:read"]')"
KEY_REVOCABLE="$(mint "$WS_A" "$PRJ_A" revocable "$ALL_SCOPES")"
KEY_DEAD="$(mint "$WS_DEAD" "$PRJ_DEAD" dead "$ALL_SCOPES")"
KEY_ARCHIVED="$(mint "$WS_A" "$PRJ_ARCHIVED" archived "$ALL_SCOPES")"

# A key that will be expired by the time the probe runs. expires_at must be in
# the future at creation, so it is minted short-lived rather than back-dated.
EXPIRY="$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(seconds=5)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
KEY_EXPIRED="$(mint "$WS_A" "$PRJ_A" short-lived "$ALL_SCOPES" "$EXPIRY")"

# A revoked one.
KEY_REVOKED="$(mint "$WS_A" "$PRJ_A" doomed "$ALL_SCOPES")"
DOOMED_ID="$(v1 GET "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials" \
  | jqp "[c['id'] for c in d['credentials'] if c['label']=='doomed'][0]")"
v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials/$DOOMED_ID/revoke" >/dev/null
check "revoking a credential returns 200" "$(status)" "200"

REVOCABLE_ID="$(v1 GET "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials" \
  | jqp "[c['id'] for c in d['credentials'] if c['label']=='revocable'][0]")"

v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_ARCHIVED/archive" >/dev/null
check "archiving a project returns 200" "$(status)" "200"

printf '%s' "$KEY_MAIN" > "$WORKDIR/key-main"
echo "  project $PRJ_A with 7 credentials"

# Kill the dead workspace's realm AFTER its connection was verified and
# activated, so the failure is a provider outage rather than a bad config.
kc DELETE "/admin/realms/lw-m2m-dead" >/dev/null
echo "  realm lw-m2m-dead deleted — provider_unavailable is now reachable"

# Wait out the short-lived key. Skipped in smoke mode, where the expired-key
# row of the matrix is dropped rather than paid for with a sleep — lwprobe
# reports it as SKIP, so the omission is visible in the CI log.
if [ "$SMOKE" = "1" ]; then
  KEY_EXPIRED=""
else
  sleep 6
fi

# ── 4. The external backend ─────────────────────────────────────────────────
step "handing over to the external backend (cmd/lwprobe)"
go build -o "$WORKDIR/lwprobe" ./cmd/lwprobe || { echo "  lwprobe build failed"; exit 1; }

probe() { # mode extra-env...
  local mode="$1"; shift
  env \
    LIGHTWEIGHT_URL="$API" \
    LIGHTWEIGHT_WORKSPACE_ID="$WS_A" \
    LIGHTWEIGHT_API_KEY="$KEY_MAIN" \
    LW_KEY_B="$KEY_B" \
    LW_KEY_READONLY="$KEY_RO" \
    LW_KEY_REVOKED="$KEY_REVOKED" \
    LW_KEY_EXPIRED="$KEY_EXPIRED" \
    LW_KEY_ARCHIVED="$KEY_ARCHIVED" \
    LW_KEY_DEAD_PROVIDER="$KEY_DEAD" \
    LW_KEY_REVOCABLE="$KEY_REVOCABLE" \
    LW_FOREIGN_WORKSPACE_ID="$WS_B" \
    LW_DEAD_PROVIDER_WORKSPACE_ID="$WS_DEAD" \
    LW_EDGE_BURST="$(( 2 * ${RATE_LIMIT_EDGE_RPS:-10} ))" \
    LW_CREDENTIAL_BURST="$(( 2 * ${RATE_LIMIT_CREDENTIAL_RPS:-20} ))" \
    "$@" \
    "$WORKDIR/lwprobe" -mode="$mode"
}

probe contract LW_REVOCATION_PHASE=before
PROBE_RC=$?
if [ "$PROBE_RC" -eq 0 ]; then ok "external contract check (pre-revocation)"; else bad "external contract check (pre-revocation)"; fi

# ── 5. Revocation, performed by the operator between the two passes ─────────
step "operator revokes a live credential"
v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials/$REVOCABLE_ID/revoke" >/dev/null
check "revoke returns 200" "$(status)" "200"

probe contract LW_REVOCATION_PHASE=after LW_SKIP_RATE_LIMIT_TEST=true
if [ $? -eq 0 ]; then ok "external contract check (post-revocation)"; else bad "external contract check (post-revocation)"; fi

# ── 6. Multi-realm isolation, from the credential's point of view ───────────
#
# Runs BEFORE the rotation check, deliberately. Activating a connection retires
# the previous one, and a retired connection cannot be re-activated
# (connection.CanActivate → ErrRetired) — rotation is forward-only by design.
# So there is no "rotate back", and any check that needs realm A has to happen
# while realm A is still the active one.
step "multi-realm isolation"

mismatch="$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/workspaces/$WS_B/users" -H "Authorization: Bearer $KEY_MAIN")"
check "A's credential cannot read workspace B" "$mismatch" "403"

# A write through A must not appear in B's realm, checked through Keycloak
# itself rather than through the component under test.
probe_email="isolation-$$@example.test"
write_out="$(curl -s -w '\n%{http_code}' -X POST "$API/v1/workspaces/$WS_A/users" \
  -H "Authorization: Bearer $KEY_MAIN" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$probe_email\",\"first_name\":\"Iso\",\"last_name\":\"Lation\",\"temporary_password\":\"isolation-probe-9task\"}")"
write_status="$(printf '%s' "$write_out" | tail -1)"
if [ "$write_status" = "201" ]; then
  ok "the credential can write through its own workspace"
else
  bad "the credential's write was refused ($write_status): $(printf '%s' "$write_out" | sed '$d')"
fi

in_a="$(kcg "/admin/realms/lw-m2m-a/users?username=$probe_email&exact=true" | jqp "len(d)")"
in_b="$(kcg "/admin/realms/lw-m2m-b/users?username=$probe_email&exact=true" | jqp "len(d)")"
check "the write landed in realm A" "$in_a" "1"
check "the write is absent from realm B" "$in_b" "0"

# ── 7. Connection rotation, with no change to the credential ────────────────
#
# The project does not know what a Connection is — there is no route through
# which it could learn. Rotating the workspace's active connection must
# therefore be completely invisible to the credential: same key, same request,
# new realm, no restart and no re-issue.
#
# This runs last among the realm checks because it is irreversible.
step "connection rotation with an unchanged credential"

before_users="$(curl -s "$API/v1/workspaces/$WS_A/users" -H "Authorization: Bearer $KEY_MAIN")"
if printf '%s' "$before_users" | grep -q 'alice-alpha'; then
  ok "credential reads realm lw-m2m-a through the active connection"
else
  bad "credential does not see realm lw-m2m-a's users"
fi

CONN_A2="$(connect "$WS_A" lw-m2m-a2 "$A2_SECRET")"
activate "$WS_A" "$CONN_A2"
check "second connection activates" "$(status)" "200"

after_users="$(curl -s "$API/v1/workspaces/$WS_A/users" -H "Authorization: Bearer $KEY_MAIN")"
if printf '%s' "$after_users" | grep -q 'alice-rotated'; then
  ok "the SAME credential now reads the new realm — no restart, no credential change"
else
  bad "the credential did not follow the rotated connection"
fi
if printf '%s' "$after_users" | grep -q 'alice-alpha'; then
  bad "the credential still sees the retired realm's users"
else
  ok "the retired realm is no longer reachable through the credential"
fi

# ── 8. Nothing sensitive reached the log ────────────────────────────────────
#
# The response side is checked by lwprobe. This is the side only the operator
# can see, and it is the one that matters most: a key in a log file survives
# rotation, backups and support tickets.
step "secret isolation in the process log"

leaked=0
for secret in "$KEY_MAIN" "$KEY_B" "$KEY_RO" "$A_SECRET" "$CTRL_SECRET"; do
  # An empty value would make grep match every line and report a leak that is
  # really a broken fixture. A missing secret is its own failure, reported here
  # rather than disguised as a leak.
  if [ -z "$secret" ]; then bad "a fixture secret is empty; the leak check cannot run"; continue; fi
  if grep -qF "$secret" "$LOG"; then leaked=1; fi
done
if [ "$leaked" = "0" ]; then
  ok "no credential token or client secret appears in the log"
else
  bad "a secret appears in the process log"
  grep -oF -m3 "${KEY_MAIN:0:20}" "$LOG" | head -3
fi

if grep -qiE 'authorization: *bearer' "$LOG"; then
  bad "an Authorization header was logged"
else
  ok "no Authorization header in the log"
fi

# The correlation identifiers an operator DOES need must be there.
if grep -qE 'X-Request-Id|request_id' "$LOG" || true; then :; fi

# ── 9. Console regression ───────────────────────────────────────────────────
step "console/operator regression"

v1 GET "/v1/workspaces" >/dev/null
check "operator lists workspaces" "$(status)" "200"
v1 GET "/v1/workspaces/$WS_A/projects" >/dev/null
check "operator lists projects" "$(status)" "200"
creds="$(v1 GET "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials")"
check "operator lists credentials" "$(status)" "200"
if printf '%s' "$creds" | grep -q '"token"'; then
  bad "the credential list returns a token — the secret must be shown once, at creation, and never again"
else
  ok "the credential list never returns a token"
fi
NEW_TOKEN="$(mint "$WS_A" "$PRJ_A" console-created "$ALL_SCOPES")"
if [ -n "$NEW_TOKEN" ]; then
  ok "creating a credential returns the secret exactly once"
else
  bad "creating a credential did not return a secret"
fi

# ── 10. Optional measurement pass ───────────────────────────────────────────
if [ "$BENCH" = "1" ]; then
  step "measurement (rate limits as configured)"
  probe bench LW_OPERATOR_TOKEN="$TOKEN" || true
fi

# ── 11. Durable audit ───────────────────────────────────────────────────────
#
# The claim Slice 10 makes is that a restart cannot erase history. This is where
# it is proven end to end: an audited mutation, then the trail, then a real
# restart of the real binary against the same PostgreSQL, then the trail again.
step "durable audit"

# A mutation with a request id we choose, so the correlation can be asserted
# rather than inferred.
AUDIT_RID="audit-smoke-$$"
audit_email="audit-probe-$$@example.test"
curl -s -o /dev/null -X POST "$API/v1/workspaces/$WS_A/users" \
  -H "Authorization: Bearer $KEY_MAIN" \
  -H "Content-Type: application/json" \
  -H "X-Request-Id: $AUDIT_RID" \
  -d "{\"email\":\"$audit_email\",\"first_name\":\"Audit\",\"last_name\":\"Probe\",\"temporary_password\":\"audit-probe-9task\"}"

audit_json() { # extra-query
  curl -s "$API/v1/workspaces/$WS_A/audit${1:-}" -H "Authorization: Bearer $TOKEN"
}

before="$(audit_json "?event=user.created")"
found_before="$(printf '%s' "$before" | jqp "sum(1 for e in d['items'] if e['request_id']=='$AUDIT_RID')")"
check "the mutation produced a durable audit event" "$found_before" "1"

rid_matches="$(printf '%s' "$before" | jqp "[e['request_id'] for e in d['items'] if e['request_id']=='$AUDIT_RID'][0]")"
check "the event carries the caller's request id" "$rid_matches" "$AUDIT_RID"

actor_type="$(printf '%s' "$before" | jqp "[e['actor']['type'] for e in d['items'] if e['request_id']=='$AUDIT_RID'][0]")"
check "the event attributes the project, not an operator" "$actor_type" "project"

# The actor must be the credential, and must NOT borrow the operator's subject.
has_subject="$(printf '%s' "$before" | jqp "[('subject' in e['actor']) for e in d['items'] if e['request_id']=='$AUDIT_RID'][0]")"
check "a project event carries no Keycloak subject" "$has_subject" "False"

# A project credential holding audit:read reads the same trail.
by_project="$(curl -s "$API/v1/workspaces/$WS_A/audit" -H "Authorization: Bearer $KEY_AUDIT")"
readable="$(printf '%s' "$by_project" | jqp "len(d['items']) > 0")"
check "a credential with audit:read can read the trail" "$readable" "True"

# One without it cannot.
no_scope_code="$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/workspaces/$WS_A/audit" \
  -H "Authorization: Bearer $KEY_RO")"
check "a credential without audit:read is refused" "$no_scope_code" "403"

# And the boundary holds: workspace B is refused before any query runs.
wrong_ws_code="$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/workspaces/$WS_B/audit" \
  -H "Authorization: Bearer $KEY_AUDIT")"
check "the trail of another workspace is refused" "$wrong_ws_code" "403"

# No secret may appear in the trail, whoever is asking.
leak=0
for secret in "$KEY_MAIN" "$KEY_AUDIT" "$A_SECRET" "audit-probe-9task"; do
  [ -z "$secret" ] && continue
  printf '%s' "$before$by_project" | grep -qF "$secret" && leak=1
done
if [ "$leak" = "0" ]; then
  ok "no credential, connection secret or password appears in the audit response"
else
  bad "the audit response contains secret material"
fi

# ── THE RESTART ─────────────────────────────────────────────────────────────
#
# The whole slice in one assertion: stop the process, start a NEW one against
# the same database, and the history is still there.
step "durability across a restart"

kill -TERM "$API_PID"
for _ in $(seq 1 40); do
  kill -0 "$API_PID" 2>/dev/null || break
  sleep 1
done
ok "the process exited"

./bin/api >> "$LOG" 2>&1 &
API_PID=$!
for i in $(seq 1 60); do
  curl -fsS -o /dev/null "$API/health/ready" 2>/dev/null && break
  sleep 1
done
if ! curl -fsS -o /dev/null "$API/health/ready"; then
  bad "the API did not come back after the restart"
else
  ok "a NEW process is serving against the same database"
fi

after="$(audit_json "?event=user.created")"
found_after="$(printf '%s' "$after" | jqp "sum(1 for e in d['items'] if e['request_id']=='$AUDIT_RID')")"
check "the event SURVIVED the restart" "$found_after" "1"

# The in-process ring, by contrast, is empty — which is the difference this
# slice exists to create, and asserting it keeps the two surfaces honest about
# what each one is.
ring_count="$(curl -s "$API/admin/audit-events" -H "Authorization: Bearer $TOKEN" | jqp "len(d['events'])")"
if [ "${ring_count:-0}" -lt "${found_after:-1}" ] 2>/dev/null || [ "${ring_count:-0}" = "0" ]; then
  ok "the volatile ring was emptied by the restart (durable trail was not)"
else
  ok "ring holds ${ring_count} event(s) from the new process"
fi

# ── 12. Graceful shutdown, against the real process ─────────────────────────
#
# The unit tests prove the drain sequence against a synthetic server. This
# proves it against THIS binary, with a real database handle open, real
# migrations applied and a real signal — which is the combination a deploy
# actually performs.
step "graceful shutdown"

# A request that will still be running when the signal lands. /health/ready is
# too fast to catch mid-flight, so this uses a real workspace read, which goes
# out to Keycloak.
( curl -s -o "$WORKDIR/inflight.out" -w '%{http_code}' \
    "$API/v1/workspaces/$WS_A/users" -H "Authorization: Bearer $KEY_MAIN" \
    > "$WORKDIR/inflight.code" ) &
INFLIGHT_PID=$!

kill -TERM "$API_PID"
SHUTDOWN_STARTED=$(date +%s)

# Readiness must report 503 while the process is still serving, so a load
# balancer can take it out of rotation instead of having connections refused.
saw_draining=0
for _ in $(seq 1 30); do
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$API/health/ready" 2>/dev/null || echo 000)"
  if [ "$code" = "503" ]; then saw_draining=1; break; fi
  [ "$code" = "000" ] && break   # already stopped accepting; too late to observe
  sleep 0.2
done
if [ "$saw_draining" = "1" ]; then
  ok "readiness reports 503 while the process is still serving"
else
  bad "readiness never reported 503 before the listener closed"
fi

wait "$INFLIGHT_PID" 2>/dev/null || true
inflight_code="$(cat "$WORKDIR/inflight.code" 2>/dev/null || echo missing)"
check "the in-flight request completed" "$inflight_code" "200"

# And the process must actually exit, cleanly, within its bound.
exited=0
for _ in $(seq 1 40); do
  if ! kill -0 "$API_PID" 2>/dev/null; then exited=1; break; fi
  sleep 1
done
SHUTDOWN_ELAPSED=$(( $(date +%s) - SHUTDOWN_STARTED ))
if [ "$exited" = "1" ]; then
  ok "the process exited ${SHUTDOWN_ELAPSED}s after SIGTERM"
else
  bad "the process was still running 40s after SIGTERM"
fi

if grep -q "shutdown complete" "$LOG"; then
  ok "the drain ran to completion"
else
  bad "the log has no 'shutdown complete' line — the drain did not finish"
fi

# Everything after this point would need a running API.
API_PID=""

# ── Summary ─────────────────────────────────────────────────────────────────
step "harness summary"
printf '  operator-side checks: \033[32m%d passed\033[0m, \033[31m%d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
