#!/usr/bin/env bash
# sdk-acceptance.sh — stand up a real LIGHTWEIGHT installation, then run the
# official Go SDK against it as a genuinely external backend.
#
# It answers the Slice 13 question: is the SDK's abstraction real, or does it
# only look real against fixtures?
#
# ─── The split, which is the point ──────────────────────────────────────────
#
# This script is the OPERATOR. It knows the identity provider exists: it builds
# tenants, creates workspaces, wires connections, mints and revokes Project
# Credentials. That is the setup an installer does once, by hand or through the
# console.
#
#   sdk/go/acceptance is the BACKEND. It is a separate Go module that cannot
#   import this repository at all, and it receives three environment variables.
#   Everything it proves, it proves through the SDK's exported API.
#
# If this script ever has to hand the SDK suite a provider URL, a tenant name or
# a client secret to get a check passing, the architectural claim has failed and
# THAT is the finding — not something to work around.
#
# ─── Usage ──────────────────────────────────────────────────────────────────
#
#   DB_URL=postgres://…  ./scripts/sdk-acceptance.sh
#   DB_URL=postgres://…  ./scripts/sdk-acceptance.sh --keep
#
# Environment:
#   DB_URL                    required; an EMPTY database
#   KEYCLOAK_VERIFY_URL       default http://localhost:8081
#   KEYCLOAK_VERIFY_ADMIN     default admin
#   KEYCLOAK_VERIFY_PASSWORD  default admin
#   API_PORT                  default 58091   (m2m-harness uses 58090)
#
# > **Point DB_URL at a throwaway database.** Workspaces are created with fixed
# > names, and nothing is dropped for you.
#
# ─── What it creates, and removes on exit ───────────────────────────────────
#
#   lw-sdk-control  the installation tenant the OPERATOR authenticates against
#   lw-sdk-a        workspace A's tenant — the SDK's own
#   lw-sdk-b        workspace B's tenant — one the SDK must never reach
#   lw-sdk-frozen   the tenant behind a workspace the operator archives, so the
#                   SDK meets the inactive-parent state (Slice 14 / KI-018)
#   lw-sdk-limited  a tenant whose service account loses its write privileges
#                   after verification, so the SDK meets provider_forbidden
set -uo pipefail

KEEP=0
for arg in "$@"; do
  case "$arg" in
    --keep) KEEP=1 ;;
    *) echo "unknown flag: $arg"; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KC="${KEYCLOAK_VERIFY_URL:-http://localhost:8081}"
KC_ADMIN="${KEYCLOAK_VERIFY_ADMIN:-admin}"
KC_PASS="${KEYCLOAK_VERIFY_PASSWORD:-admin}"
API_PORT="${API_PORT:-58091}"
API="http://localhost:${API_PORT}"
DB="${DB_URL:?DB_URL required}"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/lw-sdk-acceptance.XXXXXX")"
LOG="$WORKDIR/api.log"
REVOKE_SIGNAL="$WORKDIR/revoke-now"

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi; }

# shellcheck source=lib/keycloak-fixture.sh
. "$REPO_ROOT/scripts/lib/keycloak-fixture.sh"

kc_authenticate || exit 1

REALMS="lw-sdk-control lw-sdk-a lw-sdk-b lw-sdk-frozen lw-sdk-limited"

cleanup() {
  if [ -n "${LW_DIAGNOSTICS_DIR:-}" ] && [ -f "$LOG" ]; then
    mkdir -p "$LW_DIAGNOSTICS_DIR"
    cp "$LOG" "$LW_DIAGNOSTICS_DIR/lightweight-api-sdk.log" 2>/dev/null || true
  fi

  # The revocation watcher must die before the workdir it polls does.
  [ -n "${WATCHER_PID:-}" ] && kill "$WATCHER_PID" 2>/dev/null

  if [ "$KEEP" = "1" ]; then
    step "left running (--keep)"
    cat <<EOF
  api          ${API}
  workspace A  ${WS_A:-?}
  api key      ${KEY_MAIN:0:14}…   (full value in $WORKDIR/key-main)
  api log      ${LOG}
  api pid      ${API_PID:-none}
EOF
    return
  fi
  step "cleanup (only what this run created)"
  [ -n "${API_PID:-}" ] && kill "$API_PID" 2>/dev/null && echo "  stopped api pid $API_PID"
  KCT="$(kct)"
  for r in $REALMS; do kc DELETE "/admin/realms/$r" >/dev/null; done
  echo "  deleted fixture tenants"
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

# ── 1. Provider fixtures ────────────────────────────────────────────────────
step "building disposable provider tenants"
for r in $REALMS; do make_realm "$r"; done

kc POST /admin/realms/lw-sdk-control/roles '{"name":"admin"}' >/dev/null
kc POST /admin/realms/lw-sdk-control/clients \
  '{"clientId":"lw-console","enabled":true,"publicClient":true,"standardFlowEnabled":true,"directAccessGrantsEnabled":true}' >/dev/null
OP_ID="$(make_user lw-sdk-control operator "operator-pw")"
ADMIN_ROLE="$(kcg /admin/realms/lw-sdk-control/roles/admin)"
kc POST "/admin/realms/lw-sdk-control/users/$OP_ID/role-mappings/realm" "[$ADMIN_ROLE]" >/dev/null
CTRL_SECRET="$(make_sa_client lw-sdk-control lw-api-admin realm-admin)"

# One distinctive user per tenant. These two names are what the SDK's isolation
# test uses to tell the tenants apart, and they are the only thing about the
# provider it is ever told — as opaque strings that happen to be usernames.
#
# Bare usernames, not addresses: make_user derives the email by appending
# @example.test, so an address here would produce a doubled domain and a create
# the provider rejects — leaving both tenants empty and the isolation assertion
# passing for the wrong reason.
USER_A="alice-in-a"
USER_B="bob-in-b"
A_ID="$(make_user lw-sdk-a "$USER_A" "fixture-pw-9task")"
B_ID="$(make_user lw-sdk-b "$USER_B" "fixture-pw-9task")"
if [ -z "$A_ID" ] || [ -z "$B_ID" ]; then
  echo "  ✗ the fixture users were not created; the isolation check would pass vacuously"
  exit 1
fi

A_SECRET="$(make_sa_client lw-sdk-a lw-conn realm-admin)"
B_SECRET="$(make_sa_client lw-sdk-b lw-conn realm-admin)"
FROZEN_SECRET="$(make_sa_client lw-sdk-frozen lw-conn realm-admin)"

# The provider-forbidden fixture is created with FULL privileges and loses them
# after its connection is verified — see strip_sa_roles below for why the
# obvious construction does not reach the code path being tested.
LIMITED_SECRET="$(make_sa_client lw-sdk-limited lw-conn realm-admin)"
echo "  tenants + service accounts ready"

# strip_sa_roles — take write privileges away from a service account after the
# connection has already been verified and activated.
#
# Granting only `view-users` from the start would NOT produce provider_forbidden:
# verification would record the connection as read_only and the runtime's local
# pre-flight guard would refuse the write with connection_read_only before
# Keycloak was ever asked. Removing the roles afterwards leaves the recorded
# access mode stale, which is the state a real installation drifts into when an
# administrator tightens a realm and nobody re-runs verify — and it is the only
# way the provider itself gets to do the refusing.
strip_sa_roles() { # realm clientId
  local realm="$1" cid="$2" iid su mgmt roles remove keep
  iid="$(kcg "/admin/realms/$realm/clients?clientId=$cid" | jqp "d[0]['id']")"
  su="$(kcg "/admin/realms/$realm/clients/$iid/service-account-user" | jqp "d['id']")"
  mgmt="$(kcg "/admin/realms/$realm/clients?clientId=realm-management" | jqp "d[0]['id']")"
  roles="$(kcg "/admin/realms/$realm/clients/$mgmt/roles")"
  remove="$(printf '%s' "$roles" | python3 -c "
import sys,json
print(json.dumps([r for r in json.load(sys.stdin) if r['name'] == 'realm-admin']))")"
  kc DELETE "/admin/realms/$realm/users/$su/role-mappings/clients/$mgmt" "$remove" >/dev/null
  keep="$(printf '%s' "$roles" | python3 -c "
import sys,json
want={'view-users','view-realm'}
print(json.dumps([r for r in json.load(sys.stdin) if r['name'] in want]))")"
  kc POST "/admin/realms/$realm/users/$su/role-mappings/clients/$mgmt" "$keep" >/dev/null
}

# ── 2. Boot the API ─────────────────────────────────────────────────────────
step "booting the API"

if curl -fsS -o /dev/null "$API/health" 2>/dev/null; then
  echo "  ✗ something is already listening on port $API_PORT."
  echo "    Stop it first, or set API_PORT to a free port."
  exit 1
fi

make -s -C "$REPO_ROOT" build >/dev/null || { echo "  build failed"; exit 1; }

export DB_URL="$DB" \
  DB_MIGRATE_ON_BOOT=true \
  PORT="$API_PORT" \
  KEYCLOAK_URL="$KC" \
  KEYCLOAK_REALM=lw-sdk-control \
  KEYCLOAK_CLIENT_ID=lw-console \
  KEYCLOAK_CLIENT_SECRET=unused \
  KEYCLOAK_JWKS_URL="$KC/realms/lw-sdk-control/protocol/openid-connect/certs" \
  KEYCLOAK_ALLOWED_CLIENT_IDS=lw-console \
  KEYCLOAK_ADMIN_CLIENT_ID=lw-api-admin \
  KEYCLOAK_ADMIN_CLIENT_SECRET="$CTRL_SECRET" \
  SECRETS_MASTER_KEY="$(python3 -c 'import base64;print(base64.b64encode(b"slice13-sdk-acceptance-key-32byt").decode())')" \
  ADMIN_CONSOLE_ENABLED=false \
  DEV_PLAYGROUND_ENABLED=false \
  GIN_ACCESS_LOG_ENABLED=true

"$REPO_ROOT/bin/api" > "$LOG" 2>&1 &
API_PID=$!
for i in $(seq 1 60); do
  curl -fsS -o /dev/null "$API/health/ready" 2>/dev/null && { echo "  api ready after ${i}s (pid $API_PID)"; break; }
  sleep 1
done
if ! curl -fsS -o /dev/null "$API/health/ready"; then
  echo "  api never became ready:"; tail -40 "$LOG"; exit 1
fi

TOKEN="$(curl -s -X POST "$KC/realms/lw-sdk-control/protocol/openid-connect/token" \
  -d "grant_type=password&client_id=lw-console&username=operator&password=operator-pw" | jqp "d['access_token']")"
[ -n "$TOKEN" ] || { echo "  operator token failed"; exit 1; }
echo "  operator authenticated"

STATUS_FILE="$WORKDIR/.v1_status"
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

# ── 3. Operator setup ───────────────────────────────────────────────────────
step "operator sets up two workspaces"

WS_A="$(v1 POST /v1/workspaces '{"name":"SDK Tenant Alpha"}' | jqp "d['id']")"
WS_B="$(v1 POST /v1/workspaces '{"name":"SDK Tenant Bravo"}' | jqp "d['id']")"
echo "  workspaces A=$WS_A B=$WS_B"

connect() { # workspace tenant secret → connection id
  v1 POST "/v1/workspaces/$1/connections" \
    "{\"name\":\"$2\",\"base_url\":\"$KC\",\"realm\":\"$2\",\"client_id\":\"lw-conn\",\"client_secret\":\"$3\"}" | jqp "d['id']"
}
activate() { v1 POST "/v1/workspaces/$1/connections/$2/verify" >/dev/null; v1 POST "/v1/workspaces/$1/connections/$2/activate" >/dev/null; }

CONN_A="$(connect "$WS_A" lw-sdk-a "$A_SECRET")"; activate "$WS_A" "$CONN_A"
CONN_B="$(connect "$WS_B" lw-sdk-b "$B_SECRET")"; activate "$WS_B" "$CONN_B"

WS_FROZEN="$(v1 POST /v1/workspaces '{"name":"SDK Tenant Frozen"}' | jqp "d['id']")"
WS_LIMITED="$(v1 POST /v1/workspaces '{"name":"SDK Tenant Limited"}' | jqp "d['id']")"
CONN_F="$(connect "$WS_FROZEN" lw-sdk-frozen "$FROZEN_SECRET")"; activate "$WS_FROZEN" "$CONN_F"
CONN_L="$(connect "$WS_LIMITED" lw-sdk-limited "$LIMITED_SECRET")"; activate "$WS_LIMITED" "$CONN_L"
echo "  connections active"

PRJ_A="$(v1 POST "/v1/workspaces/$WS_A/projects" '{"name":"SDK acceptance"}' | jqp "d['id']")"
PRJ_B="$(v1 POST "/v1/workspaces/$WS_B/projects" '{"name":"SDK acceptance B"}' | jqp "d['id']")"
PRJ_FROZEN="$(v1 POST "/v1/workspaces/$WS_FROZEN/projects" '{"name":"SDK acceptance frozen"}' | jqp "d['id']")"
PRJ_LIMITED="$(v1 POST "/v1/workspaces/$WS_LIMITED/projects" '{"name":"SDK acceptance limited"}' | jqp "d['id']")"

ALL_SCOPES='["users:read","users:write","roles:read","roles:write","sessions:read","sessions:revoke","invitations:read","invitations:write","audit:read"]'

mint() { # workspace project label scopes → token
  v1 POST "/v1/workspaces/$1/projects/$2/credentials" "{\"label\":\"$3\",\"scopes\":$4}" | jqp "d['secret']"
}
credential_id() { # workspace project label → id
  v1 GET "/v1/workspaces/$1/projects/$2/credentials" \
    | jqp "[c['id'] for c in d['credentials'] if c['label']=='$3'][0]"
}

KEY_MAIN="$(mint "$WS_A" "$PRJ_A" main "$ALL_SCOPES")"
KEY_B="$(mint "$WS_B" "$PRJ_B" main-b "$ALL_SCOPES")"
# Exactly one scope, so "missing scope" is provable in five directions at once.
KEY_RO="$(mint "$WS_A" "$PRJ_A" read-only '["users:read"]')"
KEY_REVOKED="$(mint "$WS_A" "$PRJ_A" doomed "$ALL_SCOPES")"
KEY_REVOCABLE="$(mint "$WS_A" "$PRJ_A" revocable "$ALL_SCOPES")"
KEY_FROZEN="$(mint "$WS_FROZEN" "$PRJ_FROZEN" frozen "$ALL_SCOPES")"
KEY_LIMITED="$(mint "$WS_LIMITED" "$PRJ_LIMITED" limited "$ALL_SCOPES")"

v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_A/credentials/$(credential_id "$WS_A" "$PRJ_A" doomed)/revoke" >/dev/null
check "the pre-revoked credential was revoked" "$(status)" "200"

REVOCABLE_ID="$(credential_id "$WS_A" "$PRJ_A" revocable)"
printf '%s' "$KEY_MAIN" > "$WORKDIR/key-main"
echo "  7 credentials minted"

# The two Slice 14 states, arranged now that their credentials exist.
#
# Archiving happens BEFORE the SDK runs rather than mid-run: the SDK's
# live-revocation test already proves the no-restart property for a credential,
# and proving it again for a workspace would cost a second signal file and a
# second watcher for evidence scripts/negative-authz-e2e.sh already produces
# against the same code path.
v1 POST "/v1/workspaces/$WS_FROZEN/archive" >/dev/null
check "the archived-workspace fixture was archived" "$(status)" "200"

strip_sa_roles lw-sdk-limited lw-conn
echo "  the limited workspace's service account lost its write privileges"

# ── 4. The revocation watcher ───────────────────────────────────────────────
#
# The SDK suite proves that revocation takes effect on a LIVE client with no
# restart. That requires the revocation to happen while its process is running —
# and the SDK process must not hold the operator token to do it itself, since not
# needing one is half of what is being proven.
#
# So the suite touches a file when it is ready, and this watcher — which does
# hold the token — performs the revocation on the other side of that boundary.
step "arming the revocation watcher"
(
  for _ in $(seq 1 300); do
    if [ -f "$REVOKE_SIGNAL" ]; then
      curl -s -o /dev/null -X POST \
        "$API/v1/workspaces/$WS_A/projects/$PRJ_A/credentials/$REVOCABLE_ID/revoke" \
        -H "Authorization: Bearer $TOKEN"
      exit 0
    fi
    sleep 0.2
  done
) &
WATCHER_PID=$!
echo "  watching $REVOKE_SIGNAL (pid $WATCHER_PID)"

# ── 5. Hand over to the SDK ─────────────────────────────────────────────────
step "handing over to the external backend (sdk/go/acceptance)"

# Everything the SDK suite is told. Note what is NOT here: no provider URL, no
# tenant name, no client id, no client secret, no operator token, no DB_URL.
# `env -i`-style hygiene is not used because `go test` needs PATH and HOME, but
# the suite itself poisons the forbidden variables and re-runs the happy path.
(
  cd "$REPO_ROOT/sdk/go" || exit 1
  env \
    LIGHTWEIGHT_URL="$API" \
    LIGHTWEIGHT_WORKSPACE_ID="$WS_A" \
    LIGHTWEIGHT_API_KEY="$KEY_MAIN" \
    LW_SDK_WORKSPACE_B="$WS_B" \
    LW_SDK_API_KEY_B="$KEY_B" \
    LW_SDK_API_KEY_READONLY="$KEY_RO" \
    LW_SDK_API_KEY_REVOKED="$KEY_REVOKED" \
    LW_SDK_API_KEY_REVOCABLE="$KEY_REVOCABLE" \
    LW_SDK_REVOKE_SIGNAL_FILE="$REVOKE_SIGNAL" \
    LW_SDK_USER_A="$USER_A" \
    LW_SDK_USER_B="$USER_B" \
    LW_SDK_WORKSPACE_ARCHIVED="$WS_FROZEN" \
    LW_SDK_API_KEY_ARCHIVED_WS="$KEY_FROZEN" \
    LW_SDK_WORKSPACE_PROVIDER_READONLY="$WS_LIMITED" \
    LW_SDK_API_KEY_PROVIDER_READONLY="$KEY_LIMITED" \
    LW_SDK_FOREIGN_USER_ID="$B_ID" \
    LW_SDK_CREDENTIAL_BURST="${RATE_LIMIT_CREDENTIAL_RPS:-20}" \
    go test -tags acceptance -count=1 -v -timeout 10m ./acceptance/...
)
SDK_RC=$?
if [ "$SDK_RC" -eq 0 ]; then ok "SDK acceptance suite"; else bad "SDK acceptance suite (exit $SDK_RC)"; fi

# ── 6. Secret isolation in the process log ──────────────────────────────────
#
# The response side is checked inside the SDK suite. This is the side only the
# operator can see, and it is the one that matters most: a key in a log file
# survives rotation, backups and support tickets.
step "secret isolation in the process log"

leaked=0
for secret in "$KEY_MAIN" "$KEY_B" "$KEY_RO" "$KEY_REVOCABLE" "$KEY_FROZEN" "$KEY_LIMITED" \
              "$A_SECRET" "$B_SECRET" "$FROZEN_SECRET" "$LIMITED_SECRET" "$CTRL_SECRET" "acceptance-temp-9task"; do
  if [ -z "$secret" ]; then bad "a fixture secret is empty; the leak check cannot run"; continue; fi
  if grep -qF "$secret" "$LOG"; then leaked=1; echo "    leaked: ${secret:0:14}…"; fi
done
if [ "$leaked" = "0" ]; then
  ok "no credential, connection secret or temporary password appears in the log"
else
  bad "a secret appears in the process log"
fi

if grep -qiE 'authorization: *bearer' "$LOG"; then
  bad "an Authorization header was logged"
else
  ok "no Authorization header in the log"
fi

# The SDK identifies itself, which is what an operator correlates against.
if grep -q 'lightweight-go/' "$LOG"; then
  ok "the SDK's User-Agent is visible in the access log"
else
  echo "  note: no lightweight-go/ User-Agent in the log (access logging may be off)"
fi

# ── Summary ─────────────────────────────────────────────────────────────────
step "harness summary"
printf '  operator-side checks: \033[32m%d passed\033[0m, \033[31m%d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
