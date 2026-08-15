# keycloak-fixture.sh — the Keycloak admin primitives every LIGHTWEIGHT e2e
# fixture needs. Sourced, never executed.
#
# ─── Why this file exists ───────────────────────────────────────────────────
#
# scripts/m2m-harness.sh grew these helpers first, and scripts/browser-e2e.sh
# needs exactly the same ones: authenticate to the master realm, create a
# disposable realm, put a user in it, put a service-account client in it with
# realm-management roles. Copying them into a second script would produce two
# implementations of "how this project talks to Keycloak", and the copy is the
# one that would not get the fix when Keycloak changes an admin-API shape.
#
# So: one implementation, two callers. The two harnesses stay separate — they
# test different boundaries — but they agree about what a fixture realm is.
#
# ─── Contract ───────────────────────────────────────────────────────────────
#
# Before sourcing, the caller must set:
#
#   KC        base URL of the Keycloak admin surface (e.g. http://localhost:8081)
#   KC_ADMIN  master-realm admin username
#   KC_PASS   master-realm admin password
#
# After sourcing, the caller must call `kc_authenticate` once, which sets the
# module-level KCT (admin access token) every other helper reads. It is a
# separate step rather than a side effect of sourcing because the token expires
# and long fixtures re-acquire it before cleanup.
#
# Nothing here writes to the application database, and nothing here starts a
# process. It creates and reads Keycloak objects, and that is all.

# jqp — read JSON on stdin, print one python expression over it. `d` is the
# parsed document. Silent on malformed input so a caller can test for "".
#
# python3 rather than jq because python3 is already required by the docs gates
# and by scripts/redact-logs.sh, so this adds no tool to the contributor's
# machine.
jqp() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

# kct — mint a master-realm admin token. Prints the token, or "" on failure.
kct() {
  curl -s -X POST "$KC/realms/master/protocol/openid-connect/token" \
    -d "grant_type=password&client_id=admin-cli&username=$KC_ADMIN&password=$KC_PASS" | jqp "d['access_token']"
}

# kc_authenticate — acquire KCT or fail loudly.
#
# Failing here rather than letting every later call 401 is the difference
# between "cannot authenticate to Keycloak at http://localhost:8081" and forty
# lines of empty JSON.
kc_authenticate() {
  KCT="$(kct)"
  if [ -z "$KCT" ]; then
    echo "cannot authenticate to Keycloak at $KC" >&2
    return 1
  fi
}

# kc — admin API call that prints only the HTTP status code. For creates and
# deletes, where the body is either empty or an error nobody acts on.
kc() { # method path [body]
  local m="$1" p="$2" b="${3:-}"
  if [ -n "$b" ]; then
    curl -s -o /dev/null -w '%{http_code}' -X "$m" "$KC$p" -H "Authorization: Bearer $KCT" -H 'Content-Type: application/json' -d "$b"
  else
    curl -s -o /dev/null -w '%{http_code}' -X "$m" "$KC$p" -H "Authorization: Bearer $KCT"
  fi
}

# kcg — admin API GET that prints the body.
kcg() { curl -s "$KC$1" -H "Authorization: Bearer $KCT"; }

# make_realm — a disposable realm, deleted first so a re-run is idempotent.
#
# The DELETE is not error-checked on purpose: 404 is the expected answer on a
# clean machine and is not a failure.
make_realm() { kc DELETE "/admin/realms/$1" >/dev/null; kc POST /admin/realms "{\"realm\":\"$1\",\"enabled\":true}" >/dev/null; }

# make_user — create a user. With a password, also prints the user id.
#
# emailVerified and an empty requiredActions are deliberate: a fixture user who
# has to change their password on first login cannot be logged in by a script,
# and cannot be logged in by a browser test either.
make_user() { # realm username [password] → prints id when a password is given
  kc POST "/admin/realms/$1/users" \
    "{\"username\":\"$2\",\"email\":\"$2@example.test\",\"firstName\":\"$2\",\"lastName\":\"Test\",\"enabled\":true,\"emailVerified\":true,\"requiredActions\":[]}" >/dev/null
  if [ -n "${3:-}" ]; then
    local uid; uid="$(kcg "/admin/realms/$1/users?username=$2" | jqp "d[0]['id']")"
    kc PUT "/admin/realms/$1/users/$uid/reset-password" \
      "{\"type\":\"password\",\"value\":\"$3\",\"temporary\":false}" >/dev/null
    echo "$uid"
  fi
}

# make_sa_client — a confidential service-account client, optionally granted
# realm-management roles. Prints the client secret.
#
# This is the shape a LIGHTWEIGHT Connection points at: standard flow off,
# direct grants off, service account on. Anything else would be a client the
# product does not know how to use.
make_sa_client() { # realm clientId role... → prints secret
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

# make_sa_client_with_secret — make_sa_client, but the secret is CHOSEN rather
# than generated.
#
# Only the browser harness needs this, and it needs it for one reason: the
# secret-artifact-isolation test has to search every produced artifact for a
# value it knows in advance, and a value it knows in advance is one it picked.
# Searching for a Keycloak-generated UUID would work too, but a sentinel with a
# recognisable prefix makes a hit in a log self-explaining rather than a
# mystery string somebody has to trace back.
make_sa_client_with_secret() { # realm clientId secret role... → prints secret
  local realm="$1" cid="$2" secret="$3"; shift 3
  kc POST "/admin/realms/$realm/clients" \
    "{\"clientId\":\"$cid\",\"enabled\":true,\"publicClient\":false,\"serviceAccountsEnabled\":true,\"standardFlowEnabled\":false,\"directAccessGrantsEnabled\":false,\"secret\":\"$secret\"}" >/dev/null
  local iid; iid="$(kcg "/admin/realms/$realm/clients?clientId=$cid" | jqp "d[0]['id']")"
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

# make_browser_client — a PUBLIC client a real browser can complete an
# Authorization Code + PKCE login against.
#
# Distinct from the harness's `lw-console`, which is also public but has no
# redirect URIs and no web origins because curl's direct-access-grant needs
# neither. A browser needs both, and the failure mode when they are missing is
# not obvious: Keycloak answers "Invalid parameter: redirect_uri" on a page the
# test then tries to type a username into.
#
# `webOrigins` matters for a second reason people forget: the console POSTs the
# authorization code to Keycloak's TOKEN endpoint from JavaScript, cross-origin.
# Without the origin listed, the browser blocks that POST and the login fails
# after Keycloak has already accepted the credentials — which reads like a
# product bug and is not one.
make_browser_client() { # realm clientId origin
  local realm="$1" cid="$2" origin="$3" payload
  # A heredoc, not `python3 -c "…"`. The program below contains commas,
  # brackets and comments, and nesting a multi-line double-quoted string inside
  # a command substitution mangles it — the shell re-scans the inner quotes and
  # feeds python a fragment per line. A heredoc has no such interaction, and
  # the values arrive through the environment so nothing is interpolated into
  # source text.
  payload="$(LW_CID="$cid" LW_ORIGIN="$origin" python3 <<'PY'
import json, os
cid = os.environ["LW_CID"]
origin = os.environ["LW_ORIGIN"]
print(json.dumps({
    "clientId": cid,
    "enabled": True,
    "publicClient": True,
    "standardFlowEnabled": True,
    # Direct grants stay on: the fixture mints an operator token over HTTP to
    # provision preconditions, exactly as the m2m harness does. The BROWSER
    # never uses it — every login in a spec goes through the Keycloak UI.
    "directAccessGrantsEnabled": True,
    "redirectUris": [origin + "/admin", origin + "/admin*"],
    "webOrigins": [origin],
    "attributes": {"pkce.code.challenge.method": "S256"},
}))
PY
)"
  kc POST "/admin/realms/$realm/clients" "$payload" >/dev/null
}

# wait_for_ready — poll /health/ready until 200, or fail with diagnostics.
#
# READINESS, not liveness. Liveness answers 200 while migrations are still
# running, so a browser opened against a live-but-not-ready process would race
# the schema and fail somewhere far from the cause.
wait_for_ready() { # base_url attempts logfile
  local base="$1" attempts="${2:-60}" logfile="${3:-}"
  local i
  for i in $(seq 1 "$attempts"); do
    if curl -fsS -o /dev/null "$base/health/ready" 2>/dev/null; then
      echo "  ready after ${i}s"
      return 0
    fi
    sleep 1
  done
  echo "  never became ready. Last readiness response:" >&2
  curl -s "$base/health/ready" >&2 || true
  echo >&2
  if [ -n "$logfile" ] && [ -f "$logfile" ]; then
    echo "  api log (tail):" >&2
    tail -40 "$logfile" >&2
  fi
  return 1
}
