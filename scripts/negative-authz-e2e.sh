#!/usr/bin/env bash
# negative-authz-e2e.sh — the real-stack half of the Slice 14 negative
# authorization matrix (KI-018).
#
# It stands up a genuinely multi-tenant LIGHTWEIGHT installation — five Keycloak
# realms, four workspaces, three projects, nine credentials — and then runs
# cmd/lwprobe against it as an outsider trying to get past the boundary.
#
# ─── The split, which is the whole design ───────────────────────────────────
#
# This script is the OPERATOR. It creates realms, workspaces and connections,
# mints credentials, archives a workspace, retires a connection and revokes a
# key. Those are console actions, and a program holding a Project Credential
# must not be able to perform any of them.
#
#   cmd/lwprobe is the ATTACKER. It imports nothing from this module, receives
#   only URLs, workspace ids and credentials, and has no way to reach Keycloak
#   or the database. If it could reach either, the boundary it is probing would
#   not be the boundary a real consumer faces.
#
# ─── Why the run has two probe passes ───────────────────────────────────────
#
# Three of the properties are TRANSITIONS, not states:
#
#   a credential that worked stops working when its workspace is archived
#   a credential that worked stops working when its connection is retired
#   a credential that worked stops working when it is revoked
#
# Proving those needs the "before" as much as the "after", against the SAME
# process, with the provider cache already warm — because a cached positive
# decision outliving the change is exactly the bug being looked for. So:
#
#   pass 1 (warm)    prove they work, populate the cache
#   operator acts    archive, retire, revoke
#   pass 2 (matrix)  prove they stopped, plus the whole static matrix
#
# There is no restart between them, and nothing flushes anything.
#
# ─── Usage ──────────────────────────────────────────────────────────────────
#
#   DB_URL=postgres://…  ./scripts/negative-authz-e2e.sh
#   DB_URL=postgres://…  ./scripts/negative-authz-e2e.sh --keep
#
# Environment:
#   DB_URL                    required; an EMPTY database
#   KEYCLOAK_VERIFY_URL       default http://localhost:8081
#   KEYCLOAK_VERIFY_ADMIN     default admin
#   KEYCLOAK_VERIFY_PASSWORD  default admin
#   API_PORT                  default 58092   (m2m 58090, sdk 58091)
#
# > **Point DB_URL at a throwaway database.** Realms and workspaces are created
# > with fixed names and nothing is dropped for you.
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
API_PORT="${API_PORT:-58092}"
API="http://localhost:${API_PORT}"
DB="${DB_URL:?DB_URL required}"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/lw-negative-authz.XXXXXX")"
LOG="$WORKDIR/api.log"
PROBE_OUT="$WORKDIR/probe.log"

# The sentinel a rejected password operation carries. Unique so the artifact
# scan at the end can say, without ambiguity, that a value which existed only
# inside REFUSED requests never reached the realm, a response, or a log line.
PASSWORD_SENTINEL="lw-neg-sentinel-$$-do-not-log"

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi; }

# shellcheck source=lib/keycloak-fixture.sh
. "$REPO_ROOT/scripts/lib/keycloak-fixture.sh"

kc_authenticate || exit 1

# Five realms, and each one exists because a specific check needs it.
#
#   control   the installation realm the OPERATOR authenticates against
#   alpha     workspace A — the credential's own tenant
#   bravo     workspace B — the tenant it must never reach, and the source of
#             the foreign user id used for the cross-realm attack
#   frozen    the workspace that gets archived mid-run
#   limited   the workspace whose SERVICE ACCOUNT can read but cannot write —
#             the provider-forbidden fixture, which needs a real Keycloak to
#             actually refuse
#
# A sixth workspace (the one that loses its connection) reuses the `frozen`
# realm's shape but gets its own realm so retiring its connection cannot
# disturb the archived one.
#   solo      a realm with EXACTLY ONE enabled admin, so the last-admin guard
#             can actually be triggered. KI-018 recorded that it never had been:
#             adversarial probing failed to trigger it because the test realm
#             happened to have two admins, so the guard's runtime behaviour was
#             asserted only by unit tests against the service function.
REALMS="lw-neg-control lw-neg-alpha lw-neg-bravo lw-neg-frozen lw-neg-limited lw-neg-orphan lw-neg-solo"

cleanup() {
  if [ -n "${LW_DIAGNOSTICS_DIR:-}" ]; then
    mkdir -p "$LW_DIAGNOSTICS_DIR"
    [ -f "$LOG" ] && cp "$LOG" "$LW_DIAGNOSTICS_DIR/lightweight-api-negative-authz.log" 2>/dev/null
    [ -f "$PROBE_OUT" ] && cp "$PROBE_OUT" "$LW_DIAGNOSTICS_DIR/lwprobe-negative.log" 2>/dev/null
  fi

  if [ "$KEEP" = "1" ]; then
    step "left running (--keep)"
    cat <<EOF
  api          ${API}
  workspace A  ${WS_A:-?}
  workspace B  ${WS_B:-?}
  api log      ${LOG}
  probe log    ${PROBE_OUT}
  api pid      ${API_PID:-none}
EOF
    return
  fi
  step "cleanup (only what this run created)"
  [ -n "${API_PID:-}" ] && kill "$API_PID" 2>/dev/null && echo "  stopped api pid $API_PID"
  KCT="$(kct)"
  for r in $REALMS; do kc DELETE "/admin/realms/$r" >/dev/null; done
  echo "  deleted fixture realms"
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

# ── 1. Provider fixtures ────────────────────────────────────────────────────
step "building disposable provider realms"
for r in $REALMS; do make_realm "$r"; done

kc POST /admin/realms/lw-neg-control/roles '{"name":"admin"}' >/dev/null
kc POST /admin/realms/lw-neg-control/clients \
  '{"clientId":"lw-console","enabled":true,"publicClient":true,"standardFlowEnabled":true,"directAccessGrantsEnabled":true}' >/dev/null
OP_ID="$(make_user lw-neg-control operator "operator-pw")"
ADMIN_ROLE="$(kcg /admin/realms/lw-neg-control/roles/admin)"
kc POST "/admin/realms/lw-neg-control/users/$OP_ID/role-mappings/realm" "[$ADMIN_ROLE]" >/dev/null
CTRL_SECRET="$(make_sa_client lw-neg-control lw-api-admin realm-admin)"

# One user per tenant realm. The two ids are the raw material of the
# cross-realm attack: both are perfectly valid Keycloak user ids, and each is
# valid in exactly one realm.
USER_A_ID="$(make_user lw-neg-alpha alice-in-alpha "fixture-pw-9task")"
USER_B_ID="$(make_user lw-neg-bravo bob-in-bravo "fixture-pw-9task")"
if [ -z "$USER_A_ID" ] || [ -z "$USER_B_ID" ]; then
  echo "  ✗ the fixture users were not created; the cross-realm checks would pass vacuously"
  exit 1
fi
make_user lw-neg-frozen carol-in-frozen "fixture-pw-9task" >/dev/null
make_user lw-neg-limited dave-in-limited "fixture-pw-9task" >/dev/null
make_user lw-neg-orphan erin-in-orphan "fixture-pw-9task" >/dev/null

A_SECRET="$(make_sa_client lw-neg-alpha lw-conn realm-admin)"
B_SECRET="$(make_sa_client lw-neg-bravo lw-conn realm-admin)"
FROZEN_SECRET="$(make_sa_client lw-neg-frozen lw-conn realm-admin)"
ORPHAN_SECRET="$(make_sa_client lw-neg-orphan lw-conn realm-admin)"

# The provider-forbidden fixture, and the shape of it is deliberate.
#
# The obvious construction — a service account granted only `view-users` from
# the start — does NOT reach the code path this check is about. Verification
# would classify the connection `read_only`, and the runtime's pre-flight write
# guard would refuse the write locally with `connection_read_only` before
# Keycloak was ever asked. That is correct behaviour, and it proves a different
# thing.
#
# So the account is created with full privileges and LOSES them afterwards, in
# Keycloak, with no re-verification. That is the real operational scenario — an
# administrator tightens the realm's roles and nobody re-runs verify — and it
# is the one where access_mode is stale, the pre-flight guard passes, the write
# is genuinely attempted, and Keycloak itself answers 403. The privileges are
# removed in step 3, after the connection has been verified and activated.
LIMITED_SECRET="$(make_sa_client lw-neg-limited lw-conn realm-admin)"

# ─── The self-protection fixtures (the original KI-018) ─────────────────────
#
# Two realms, because the guards they exercise need opposite things.
#
# `solo` holds exactly ONE enabled admin. That is the fixture KI-018 asked for
# by name: the last-admin guard could not be triggered during adversarial
# probing because the realm under test had two admins, so the ONE guard standing
# between an operator and locking their organisation out of its own realm had
# never been demonstrated at runtime.
kc POST /admin/realms/lw-neg-solo/roles '{"name":"admin"}' >/dev/null
SOLO_ADMIN_ID="$(make_user lw-neg-solo only-admin "fixture-pw-9task")"
SOLO_ROLE="$(kcg /admin/realms/lw-neg-solo/roles/admin)"
kc POST "/admin/realms/lw-neg-solo/users/$SOLO_ADMIN_ID/role-mappings/realm" "[$SOLO_ROLE]" >/dev/null
# A second user WITHOUT admin, so "the realm has one enabled admin" is a fact
# about the role rather than about the realm being empty.
make_user lw-neg-solo bystander "fixture-pw-9task" >/dev/null
SOLO_SECRET="$(make_sa_client lw-neg-solo lw-conn realm-admin)"

# The control realm gets a connection client of its own, so a workspace can
# point AT the installation's own realm. That is the ordinary single-realm
# deployment, and it is the only arrangement in which the self-protection
# guards can fire at all: they compare the caller's subject against the target,
# and across two realms those can never collide.
CONTROL_CONN_SECRET="$(make_sa_client lw-neg-control lw-conn realm-admin)"
echo "  realms, users and service accounts ready"

# strip_sa_roles — take realm-management roles away from a service account
# after the fact, leaving it able to read and unable to write.
#
# It removes the composite `realm-admin` (which is what grants everything) and
# grants back the two read roles the connection still needs, so the workspace
# keeps answering reads while every write is refused by the provider.
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
  KEYCLOAK_REALM=lw-neg-control \
  KEYCLOAK_CLIENT_ID=lw-console \
  KEYCLOAK_CLIENT_SECRET=unused \
  KEYCLOAK_JWKS_URL="$KC/realms/lw-neg-control/protocol/openid-connect/certs" \
  KEYCLOAK_ALLOWED_CLIENT_IDS=lw-console \
  KEYCLOAK_ADMIN_CLIENT_ID=lw-api-admin \
  KEYCLOAK_ADMIN_CLIENT_SECRET="$CTRL_SECRET" \
  SECRETS_MASTER_KEY="$(python3 -c 'import base64;print(base64.b64encode(b"slice14-negative-authz-key-32byt").decode())')" \
  ADMIN_CONSOLE_ENABLED=false \
  DEV_PLAYGROUND_ENABLED=false \
  GIN_ACCESS_LOG_ENABLED=true

"$REPO_ROOT/bin/api" > "$LOG" 2>&1 &
API_PID=$!
wait_for_ready "$API" 60 "$LOG" || exit 1
echo "  api pid $API_PID"

TOKEN="$(curl -s -X POST "$KC/realms/lw-neg-control/protocol/openid-connect/token" \
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
step "operator builds a multi-tenant installation"

new_workspace() { v1 POST /v1/workspaces "{\"name\":\"$1\"}" | jqp "d['id']"; }
connect() { # workspace realm secret → connection id
  v1 POST "/v1/workspaces/$1/connections" \
    "{\"name\":\"$2\",\"base_url\":\"$KC\",\"realm\":\"$2\",\"client_id\":\"lw-conn\",\"client_secret\":\"$3\"}" | jqp "d['id']"
}
activate() { v1 POST "/v1/workspaces/$1/connections/$2/verify" >/dev/null; v1 POST "/v1/workspaces/$1/connections/$2/activate" >/dev/null; }

WS_A="$(new_workspace 'Negative Alpha')"
WS_B="$(new_workspace 'Negative Bravo')"
WS_FROZEN="$(new_workspace 'Negative Frozen')"
WS_LIMITED="$(new_workspace 'Negative Limited')"
WS_ORPHAN="$(new_workspace 'Negative Orphan')"
WS_SOLO="$(new_workspace 'Negative Solo Admin')"
WS_SELF="$(new_workspace 'Negative Self')"

CONN_A="$(connect "$WS_A" lw-neg-alpha "$A_SECRET")";           activate "$WS_A" "$CONN_A"
CONN_B="$(connect "$WS_B" lw-neg-bravo "$B_SECRET")";           activate "$WS_B" "$CONN_B"
CONN_F="$(connect "$WS_FROZEN" lw-neg-frozen "$FROZEN_SECRET")"; activate "$WS_FROZEN" "$CONN_F"
CONN_L="$(connect "$WS_LIMITED" lw-neg-limited "$LIMITED_SECRET")"
CONN_O="$(connect "$WS_ORPHAN" lw-neg-orphan "$ORPHAN_SECRET")"; activate "$WS_ORPHAN" "$CONN_O"
CONN_S="$(connect "$WS_SOLO" lw-neg-solo "$SOLO_SECRET")";        activate "$WS_SOLO" "$CONN_S"
CONN_SELF="$(connect "$WS_SELF" lw-neg-control "$CONTROL_CONN_SECRET")"; activate "$WS_SELF" "$CONN_SELF"

# The limited connection is verified and activated while its service account
# STILL HAS full privileges, so access_mode is recorded as `full` and the
# runtime's pre-flight write guard will let a write through. Only then are the
# privileges taken away in Keycloak.
#
# The result is the state a real installation drifts into: a connection whose
# recorded access mode is stale, and a provider that refuses. The write is
# genuinely attempted and Keycloak itself answers 403, which is the only way to
# exercise provider_forbidden rather than the local guard that shadows it.
v1 POST "/v1/workspaces/$WS_LIMITED/connections/$CONN_L/verify" >/dev/null
v1 POST "/v1/workspaces/$WS_LIMITED/connections/$CONN_L/activate" >/dev/null
LIMITED_ACTIVATION="$(status)"
if [ "$LIMITED_ACTIVATION" != "200" ]; then
  echo "  note: the limited connection did not activate (HTTP $LIMITED_ACTIVATION);"
  echo "        the provider-forbidden check will report itself skipped."
  WS_LIMITED=""
else
  strip_sa_roles lw-neg-limited lw-conn
  echo "  the limited workspace's service account lost its write privileges after activation"
fi

echo "  workspaces: A=$WS_A B=$WS_B frozen=$WS_FROZEN orphan=$WS_ORPHAN limited=${WS_LIMITED:-none}"

# Two projects in workspace A. The pair is the cross-project fixture: same
# workspace, disjoint capabilities, and neither may borrow the other's.
PRJ_A1="$(v1 POST "/v1/workspaces/$WS_A/projects" '{"name":"Negative P1"}' | jqp "d['id']")"
PRJ_A2="$(v1 POST "/v1/workspaces/$WS_A/projects" '{"name":"Negative P2"}' | jqp "d['id']")"
PRJ_B="$(v1 POST "/v1/workspaces/$WS_B/projects" '{"name":"Negative B"}' | jqp "d['id']")"
PRJ_FROZEN="$(v1 POST "/v1/workspaces/$WS_FROZEN/projects" '{"name":"Negative Frozen"}' | jqp "d['id']")"
PRJ_ORPHAN="$(v1 POST "/v1/workspaces/$WS_ORPHAN/projects" '{"name":"Negative Orphan"}' | jqp "d['id']")"

IDENTITY_SCOPES='["users:read","users:write","roles:read","roles:write","sessions:read","sessions:revoke","invitations:read","invitations:write"]'
ALL_SCOPES='["users:read","users:write","roles:read","roles:write","sessions:read","sessions:revoke","invitations:read","invitations:write","audit:read"]'

mint() { # workspace project label scopes → token
  v1 POST "/v1/workspaces/$1/projects/$2/credentials" "{\"label\":\"$3\",\"scopes\":$4}" | jqp "d['secret']"
}
credential_id() { # workspace project label → id
  v1 GET "/v1/workspaces/$1/projects/$2/credentials" \
    | jqp "[c['id'] for c in d['credentials'] if c['label']=='$3'][0]"
}

KEY_MAIN="$(mint       "$WS_A" "$PRJ_A1" main         "$ALL_SCOPES")"
KEY_RO="$(mint         "$WS_A" "$PRJ_A1" read-only    '["users:read"]')"
KEY_NO_AUDIT="$(mint   "$WS_A" "$PRJ_A1" no-audit     "$IDENTITY_SCOPES")"
KEY_AUDIT_ONLY="$(mint "$WS_A" "$PRJ_A1" audit-only   '["audit:read"]')"
KEY_P2="$(mint         "$WS_A" "$PRJ_A2" second-proj  '["roles:write"]')"
KEY_REVOCABLE="$(mint  "$WS_A" "$PRJ_A1" revocable    '["users:read"]')"
KEY_B="$(mint          "$WS_B" "$PRJ_B"  bravo        "$ALL_SCOPES")"
KEY_FROZEN="$(mint     "$WS_FROZEN" "$PRJ_FROZEN" frozen "$ALL_SCOPES")"
KEY_ORPHAN="$(mint     "$WS_ORPHAN" "$PRJ_ORPHAN" orphan "$ALL_SCOPES")"
KEY_LIMITED=""
if [ -n "$WS_LIMITED" ]; then
  PRJ_LIMITED="$(v1 POST "/v1/workspaces/$WS_LIMITED/projects" '{"name":"Negative Limited"}' | jqp "d['id']")"
  KEY_LIMITED="$(mint "$WS_LIMITED" "$PRJ_LIMITED" limited "$ALL_SCOPES")"
fi

REVOCABLE_ID="$(credential_id "$WS_A" "$PRJ_A1" revocable)"
echo "  9 credentials minted across 3 projects and 5 workspaces"

# ── 4. Hand over to the attacker: pass 1 ────────────────────────────────────
probe() { # phase
  (
    cd "$REPO_ROOT" || exit 1
    env \
      LIGHTWEIGHT_URL="$API" \
      LIGHTWEIGHT_WORKSPACE_ID="$WS_A" \
      LIGHTWEIGHT_API_KEY="$KEY_MAIN" \
      LW_KEY_B="$KEY_B" \
      LW_KEY_READONLY="$KEY_RO" \
      LW_KEY_NO_AUDIT="$KEY_NO_AUDIT" \
      LW_KEY_AUDIT_ONLY="$KEY_AUDIT_ONLY" \
      LW_KEY_SECOND_PROJECT="$KEY_P2" \
      LW_KEY_REVOCABLE="$KEY_REVOCABLE" \
      LW_KEY_ARCHIVABLE_WS="$KEY_FROZEN" \
      LW_ARCHIVABLE_WORKSPACE_ID="$WS_FROZEN" \
      LW_KEY_LOSES_CONNECTION="$KEY_ORPHAN" \
      LW_LOSES_CONNECTION_WORKSPACE_ID="$WS_ORPHAN" \
      LW_KEY_PROVIDER_READONLY="$KEY_LIMITED" \
      LW_PROVIDER_READONLY_WORKSPACE_ID="${WS_LIMITED:-}" \
      LW_FOREIGN_WORKSPACE_ID="$WS_B" \
      LW_FOREIGN_USER_ID="$USER_B_ID" \
      LW_OWN_USER_ID="$USER_A_ID" \
      LW_PASSWORD_SENTINEL="$PASSWORD_SENTINEL" \
      go run ./cmd/lwprobe -mode negative -phase "$1"
  )
}

step "pass 1 — the credentials about to be cut off currently work"
probe warm 2>&1 | tee -a "$PROBE_OUT"
WARM_RC="${PIPESTATUS[0]}"
if [ "$WARM_RC" -eq 0 ]; then ok "warm pass"; else bad "warm pass (exit $WARM_RC)"; fi

# ── 5. The operator performs the three transitions ──────────────────────────
step "operator archives, retires and revokes"

v1 POST "/v1/workspaces/$WS_FROZEN/archive" >/dev/null
check "workspace archived" "$(status)" "200"

v1 POST "/v1/workspaces/$WS_ORPHAN/connections/$CONN_O/retire" >/dev/null
check "the orphan workspace's only connection retired" "$(status)" "200"

v1 POST "/v1/workspaces/$WS_A/projects/$PRJ_A1/credentials/$REVOCABLE_ID/revoke" >/dev/null
check "credential revoked" "$(status)" "200"

# No restart, no cache flush, no sleep. If any of the three keeps working past
# this line, a cached decision is outliving the state change that ended it.

# ── 6. Pass 2 — the matrix ──────────────────────────────────────────────────
step "pass 2 — the negative authorization matrix"
probe matrix 2>&1 | tee -a "$PROBE_OUT"
PROBE_RC="${PIPESTATUS[0]}"
if [ "$PROBE_RC" -eq 0 ]; then ok "negative matrix"; else bad "negative matrix (exit $PROBE_RC)"; fi

# ── 6b. The self-protection guards, exercised by an OPERATOR ────────────────
#
# This is the original text of KI-018, and it is a different boundary from
# everything above.
#
# The guards — no self-delete, no self-disable, no self-strip-admin, no removing
# the realm's last enabled admin — protect an OPERATOR from locking their
# organisation out of its own realm. They are unreachable from the Project
# surface by construction, and that is worth stating precisely rather than
# leaving implied:
#
#   the three SELF guards compare the caller's Keycloak subject against the
#   target. A project credential has no Keycloak subject at all — the runtime
#   deliberately never manufactures one — so they can never match, and the
#   guards can never fire for a machine.
#
#   the LAST-ADMIN guard is realm-local and does fire for anyone. For a project
#   credential, the protected-role guard refuses `admin` first with
#   role_privileged, so on role operations the last-admin guard is shadowed.
#   It is reachable by a machine only through DELETE user and disable-user,
#   which the matrix pass above does not target because those need a
#   single-admin realm to mean anything.
#
# So they are checked here, with the operator token, against two realms built
# for the purpose: one whose only enabled admin is the target, and one that IS
# the installation's own realm so the caller and the target can be the same
# person.
step "self-protection guards (operator boundary, KI-018 as originally written)"

# guard <name> <method> <path> [body] — expect a 403 caller_forbidden.
guard() {
  local name="$1" method="$2" path="$3" body="${4:-}"
  v1 "$method" "$path" "$body" > "$WORKDIR/.guard_body"
  local got; got="$(status)"
  local code; code="$(jqp "d['error']['code']" < "$WORKDIR/.guard_body")"
  if [ "$got" = "403" ] && [ "$code" = "caller_forbidden" ]; then
    ok "$name (403 caller_forbidden)"
  else
    bad "$name (got HTTP $got, code '${code:-none}'; want 403 caller_forbidden)"
  fi
}

# The last-admin guard, in the realm that finally makes it triggerable.
guard "the realm's last enabled admin cannot be deleted" \
  DELETE "/v1/workspaces/$WS_SOLO/users/$SOLO_ADMIN_ID"

guard "the realm's last enabled admin cannot be disabled" \
  PATCH "/v1/workspaces/$WS_SOLO/users/$SOLO_ADMIN_ID" '{"enabled":false}'

guard "the realm's last enabled admin cannot have admin stripped" \
  DELETE "/v1/workspaces/$WS_SOLO/users/$SOLO_ADMIN_ID/roles/admin"

# And the guard actually protected something: read the realm directly.
SOLO="$(kcg "/admin/realms/lw-neg-solo/users/$SOLO_ADMIN_ID")"
if [ "$(printf '%s' "$SOLO" | jqp "d.get('id','')")" != "$SOLO_ADMIN_ID" ]; then
  bad "the last admin still exists after the refusals"
elif [ "$(printf '%s' "$SOLO" | jqp "str(d.get('enabled',False))")" != "True" ]; then
  bad "the last admin is still enabled after the refusals"
else
  ok "the last admin still exists and is still enabled"
fi

SOLO_ROLES="$(kcg "/admin/realms/lw-neg-solo/users/$SOLO_ADMIN_ID/role-mappings/realm")"
if printf '%s' "$SOLO_ROLES" | grep -q '"admin"'; then
  ok "the last admin still holds the admin role"
else
  bad "the admin role was stripped from the realm's last admin"
fi

# The three SELF guards, in a workspace pointing at the installation's own
# realm — the single-realm deployment, and the only shape in which the caller's
# subject and the target's id can be the same value.
guard "an operator cannot delete their own account" \
  DELETE "/v1/workspaces/$WS_SELF/users/$OP_ID"

guard "an operator cannot disable their own account" \
  PATCH "/v1/workspaces/$WS_SELF/users/$OP_ID" '{"enabled":false}'

guard "an operator cannot strip their own admin role" \
  DELETE "/v1/workspaces/$WS_SELF/users/$OP_ID/roles/admin"

# The operator survived all three, or the rest of this run was performed by a
# principal that should no longer have existed.
OPERATOR="$(kcg "/admin/realms/lw-neg-control/users/$OP_ID")"
if [ "$(printf '%s' "$OPERATOR" | jqp "str(d.get('enabled',False))")" = "True" ]; then
  ok "the operator's own account is intact and enabled"
else
  bad "the operator disabled themselves through a guard that was supposed to refuse"
fi

# ── 7. The realm itself, read by the operator ───────────────────────────────
#
# The probe checked state integrity through the public API, which is the view a
# consumer has. This is the view only the operator has, and it is the one that
# settles the question: does the realm actually contain what the API says it
# contains?
step "provider state, read directly from Keycloak"

KCT="$(kct)"

# Nothing the probe was refused may exist in any realm.
leaked_user=0
for realm in lw-neg-alpha lw-neg-bravo lw-neg-frozen lw-neg-orphan; do
  hits="$(kcg "/admin/realms/$realm/users?search=must-not-exist" | jqp "len(d)")"
  if [ "${hits:-0}" != "0" ]; then
    leaked_user=1
    echo "    $realm contains ${hits} user(s) from a REJECTED create"
  fi
done
if [ "$leaked_user" = "0" ]; then
  ok "no rejected user-create landed in any realm"
else
  bad "a rejected user-create landed in a realm"
fi

leaked_role=0
for realm in lw-neg-alpha lw-neg-bravo lw-neg-frozen lw-neg-orphan; do
  if kcg "/admin/realms/$realm/roles" | grep -q 'must-not-exist-role'; then
    leaked_role=1
    echo "    $realm contains a role from a REJECTED create"
  fi
done
if [ "$leaked_role" = "0" ]; then
  ok "no rejected role-create landed in any realm"
else
  bad "a rejected role-create landed in a realm"
fi

# The cross-realm attack, checked from the other side: bravo's user must still
# be there, unmodified, after workspace A tried to delete and rename it.
BOB="$(kcg "/admin/realms/lw-neg-bravo/users/$USER_B_ID")"
if [ -z "$BOB" ] || [ "$(printf '%s' "$BOB" | jqp "d.get('id','')")" != "$USER_B_ID" ]; then
  bad "the foreign realm's user survived the cross-realm attack (it is gone)"
elif [ "$(printf '%s' "$BOB" | jqp "d.get('firstName','')")" = "Taken" ]; then
  bad "the foreign realm's user was MODIFIED through another workspace"
else
  ok "the foreign realm's user is present and unmodified"
fi

# And alpha's user must be untouched by anything bravo did.
ALICE="$(kcg "/admin/realms/lw-neg-alpha/users/$USER_A_ID")"
if [ "$(printf '%s' "$ALICE" | jqp "d.get('id','')")" = "$USER_A_ID" ]; then
  ok "the credential's own realm user is present and unmodified"
else
  bad "the credential's own realm user is missing"
fi

# ── 8. Secret and sentinel isolation ────────────────────────────────────────
#
# The response side is checked inside the probe. This is the side only the
# operator can see, and it is the one that outlives the run: a value in a log
# file survives rotation, backups and support tickets.
#
# Two mechanisms, and they are not redundant. The loop below knows the exact
# values this run used, which makes a hit unambiguous. scan-artifacts.sh is the
# project's shared scanner and adds SHAPE matching — `lw_sk_` tokens, JWTs,
# Authorization headers — which catches a secret that took a path nobody
# registered. Writing a third scanner was explicitly not done.
step "secret isolation in the process log"

leaked=0
for secret in "$KEY_MAIN" "$KEY_RO" "$KEY_NO_AUDIT" "$KEY_AUDIT_ONLY" "$KEY_P2" \
              "$KEY_B" "$KEY_FROZEN" "$KEY_ORPHAN" "$KEY_REVOCABLE" \
              "$A_SECRET" "$B_SECRET" "$CTRL_SECRET" "$PASSWORD_SENTINEL"; do
  if [ -z "$secret" ]; then continue; fi
  if grep -qF "$secret" "$LOG"; then leaked=1; echo "    leaked: ${secret:0:14}…"; fi
done
if [ "$leaked" = "0" ]; then
  ok "no credential, connection secret or password sentinel appears in the process log"
else
  bad "a secret appears in the process log"
fi

# The sentinel is the strongest single check here: it existed ONLY inside
# requests that were refused, so a hit anywhere means a rejected request got
# further than it should have.
if grep -qF "$PASSWORD_SENTINEL" "$PROBE_OUT"; then
  bad "the password sentinel appears in the probe's own output"
else
  ok "the password sentinel appears in no artifact"
fi

# The shared scanner, over everything this run publishes.
#
# The sentinel directory holds the values to LOOK FOR and is created outside the
# artifact tree, so it is never itself scanned and never uploaded — which is the
# contract scan-artifacts.sh documents and the reason it takes two directories
# rather than one.
SENTINELS="$WORKDIR/sentinels"
ARTIFACTS="$WORKDIR/artifacts"
mkdir -p "$SENTINELS" "$ARTIFACTS"
: > "$SENTINELS/secrets.txt"
for secret in "$KEY_MAIN" "$KEY_RO" "$KEY_NO_AUDIT" "$KEY_AUDIT_ONLY" "$KEY_P2" \
              "$KEY_B" "$KEY_FROZEN" "$KEY_ORPHAN" "$KEY_REVOCABLE" "$KEY_LIMITED" \
              "$A_SECRET" "$B_SECRET" "$FROZEN_SECRET" "$LIMITED_SECRET" \
              "$ORPHAN_SECRET" "$SOLO_SECRET" "$CONTROL_CONN_SECRET" \
              "$CTRL_SECRET" "$PASSWORD_SENTINEL"; do
  [ -n "$secret" ] && printf '%s\n' "$secret" >> "$SENTINELS/secrets.txt"
done
cp "$PROBE_OUT" "$ARTIFACTS/lwprobe-negative.log" 2>/dev/null

if "$REPO_ROOT/scripts/scan-artifacts.sh" "$ARTIFACTS" "$SENTINELS" "$LOG"; then
  ok "the shared artifact scanner found no secret in anything this run produced"
else
  bad "the shared artifact scanner found a secret in a published artifact"
fi

if grep -qiE 'authorization: *bearer' "$LOG"; then
  bad "an Authorization header was logged"
else
  ok "no Authorization header in the log"
fi

# A rejected request must still be diagnosable. The refusals are logged as
# security events with a request id; what must NOT be there is the credential.
if grep -qE '(insufficient_scope|workspace_mismatch|operator_only)' "$LOG"; then
  ok "authorization refusals are visible to an operator in the log"
else
  echo "  note: no refusal appears in the process log (security event logging may be off)"
fi

# ── Summary ─────────────────────────────────────────────────────────────────
step "harness summary"
printf '  operator-side checks: \033[32m%d passed\033[0m, \033[31m%d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
