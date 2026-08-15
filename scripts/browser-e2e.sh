#!/usr/bin/env bash
# browser-e2e.sh — stand up a real LIGHTWEIGHT installation and drive its
# admin console with a real Chromium.
#
# ─── What this proves that nothing else does ────────────────────────────────
#
# scripts/m2m-harness.sh proves the MACHINE boundary: an external backend with
# an API key can use the product. This script proves the OPERATOR boundary: a
# human with a browser can configure the product so that the machine boundary
# can exist at all — log in through Keycloak, create a workspace, wire a
# connection, mint a credential, watch the audit trail, revoke the credential.
#
# Neither replaces the other. A credential that works but cannot be created
# through the console is not a product, and a console that renders but issues
# credentials nothing can use is not one either.
#
# ─── Usage ──────────────────────────────────────────────────────────────────
#
#   DB_URL=postgres://…  ./scripts/browser-e2e.sh
#   DB_URL=postgres://…  ./scripts/browser-e2e.sh --headed        # watch it
#   DB_URL=postgres://…  ./scripts/browser-e2e.sh --keep          # leave it up
#   DB_URL=postgres://…  ./scripts/browser-e2e.sh --reset-db      # re-run locally
#   DB_URL=postgres://…  ./scripts/browser-e2e.sh -- --grep login # pass through
#
# Environment:
#   DB_URL                    required; an EMPTY database
#   KEYCLOAK_VERIFY_URL       default http://localhost:8081
#   KEYCLOAK_VERIFY_ADMIN     default admin
#   KEYCLOAK_VERIFY_PASSWORD  default admin
#   API_PORT                  default 58095 (deliberately NOT the m2m harness's
#                             58090, so both can run on one machine)
#   LW_DIAGNOSTICS_DIR        when set, logs are copied here on exit
#
# > **Point DB_URL at a throwaway database.** Workspaces are created with
# > run-scoped names, so re-runs do not collide, but nothing is dropped for you.
#
# ─── What it creates, and removes on exit ───────────────────────────────────
#
#   lw-e2e-control  the installation realm the OPERATOR logs into, in a browser
#   lw-e2e-alpha    workspace A's realm
#   lw-e2e-bravo    workspace B's realm — must never appear under A
#
# Every container, process and realm this script creates is named by this
# script and removed by that name. It never deletes anything it did not create.
set -uo pipefail

KEEP=0
HEADED=0
RESET_DB=0
PW_ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --keep)     KEEP=1 ;;
    --headed)   HEADED=1 ;;
    --reset-db) RESET_DB=1 ;;
    --)         shift; PW_ARGS=("$@"); break ;;
    *) echo "unknown flag: $1"; exit 2 ;;
  esac
  shift
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KC="${KEYCLOAK_VERIFY_URL:-http://localhost:8081}"
KC_ADMIN="${KEYCLOAK_VERIFY_ADMIN:-admin}"
KC_PASS="${KEYCLOAK_VERIFY_PASSWORD:-admin}"
API_PORT="${API_PORT:-58095}"
API="http://localhost:${API_PORT}"
DB="${DB_URL:?DB_URL required}"

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/lw-browser-e2e.XXXXXX")"
LOG="$WORKDIR/api.log"

# ─── The artifact / sentinel split ──────────────────────────────────────────
#
# ARTIFACT_DIR is everything a CI run may publish. SENTINEL_DIR is where the
# tests record the secret values they saw, so the post-run scan can search the
# artifacts for them. The two MUST be different directories, and SENTINEL_DIR
# must not be inside ARTIFACT_DIR — otherwise the scan would be searching for
# secrets in a tree that contains the file listing them, and would report a hit
# on its own bookkeeping while a real leak elsewhere looked identical.
ARTIFACT_DIR="${LW_BROWSER_ARTIFACT_DIR:-$WORKDIR/artifacts}"
SENTINEL_DIR="$WORKDIR/sentinels"
mkdir -p "$ARTIFACT_DIR" "$SENTINEL_DIR"

step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# shellcheck source=lib/keycloak-fixture.sh
. "$ROOT/scripts/lib/keycloak-fixture.sh"

kc_authenticate || exit 1

REALMS="lw-e2e-control lw-e2e-alpha lw-e2e-bravo"

# RUN_ID scopes every name the tests create, so two runs against the same
# database do not collide on a unique index and a leftover row from a killed
# run is never mistaken for this run's.
RUN_ID="$(python3 -c 'import secrets;print(secrets.token_hex(4))')"

cleanup() {
  if [ -n "${LW_DIAGNOSTICS_DIR:-}" ]; then
    mkdir -p "$LW_DIAGNOSTICS_DIR"
    [ -f "$LOG" ] && cp "$LOG" "$LW_DIAGNOSTICS_DIR/lightweight-api.log" 2>/dev/null
    # The Playwright report and the console-error logs are diagnostics too, and
    # they are already sanitised by policy (see playwright.config.js: no trace,
    # no screenshot, no video). The scan below is what proves it.
    cp -R "$ARTIFACT_DIR/." "$LW_DIAGNOSTICS_DIR/" 2>/dev/null
  fi

  if [ "$KEEP" = "1" ]; then
    step "left running (--keep)"
    cat <<EOF
  api        ${API}
  console    ${API}/admin
  operator   operator / operator-pw   (realm lw-e2e-control)
  api log    ${LOG}
  api pid    ${API_PID:-none}
  artifacts  ${ARTIFACT_DIR}

  Stop it with:  kill ${API_PID:-<pid>}
EOF
    return
  fi

  step "cleanup (only what this run created)"
  if [ -n "${API_PID:-}" ]; then
    kill "$API_PID" 2>/dev/null && echo "  stopped api pid $API_PID"
  fi
  kc_authenticate >/dev/null 2>&1
  for r in $REALMS; do kc DELETE "/admin/realms/$r" >/dev/null; done
  echo "  deleted realms: $REALMS"
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

# ── 1. Realms ───────────────────────────────────────────────────────────────
step "building disposable realms"
for r in $REALMS; do make_realm "$r"; done

# The installation realm. The operator logs into THIS one, in a browser.
kc POST /admin/realms/lw-e2e-control/roles '{"name":"admin"}' >/dev/null
make_browser_client lw-e2e-control lw-e2e-console "$API"
OP_ID="$(make_user lw-e2e-control operator "operator-pw")"
ADMIN_ROLE="$(kcg /admin/realms/lw-e2e-control/roles/admin)"
kc POST "/admin/realms/lw-e2e-control/users/$OP_ID/role-mappings/realm" "[$ADMIN_ROLE]" >/dev/null
CTRL_SECRET="$(make_sa_client lw-e2e-control lw-e2e-api-admin realm-admin)"

# ALPHA_SECRET is a SENTINEL, not a generated value.
#
# The operator types this string into the "client secret" field of the New
# connection form in a real browser. Everything after that — the DOM, the page
# source, the API log, the Playwright report — is searched for it by
# scripts/scan-artifacts.sh. A value we chose is one we can search for; a value
# Keycloak generated would work as a search term too, but a hit on
# "3f9c1e…" tells a reader nothing, and a hit on "LW-E2E-SENTINEL-CONN-…"
# explains itself.
ALPHA_SECRET="LW-E2E-SENTINEL-CONN-${RUN_ID}"
make_sa_client_with_secret lw-e2e-alpha lw-conn "$ALPHA_SECRET" realm-admin >/dev/null
BRAVO_SECRET="$(make_sa_client lw-e2e-bravo lw-conn realm-admin)"

# Realm-exclusive users. These names are the multi-realm evidence: a user whose
# name contains "alpha" must never appear while workspace B is selected, and
# the reverse. Run-scoped so a stale realm cannot supply a false positive.
ALPHA_USER="alpha-only-${RUN_ID}"
BRAVO_USER="bravo-only-${RUN_ID}"
make_user lw-e2e-alpha "$ALPHA_USER"
make_user lw-e2e-bravo "$BRAVO_USER"
echo "  realms + clients ready (run ${RUN_ID})"

# ── 2. Boot the API ─────────────────────────────────────────────────────────
step "booting LIGHTWEIGHT"

# ─── --reset-db ─────────────────────────────────────────────────────────────
#
# OPT-IN ONLY, and it says what it is doing.
#
# The realms this script creates are deleted on exit; the rows LIGHTWEIGHT
# wrote about them are not. A second local run therefore starts with workspaces
# whose Keycloak realms no longer exist, the console auto-selects the first
# active one, and the operator's very first screen fails with a 502 from a
# provider that is genuinely gone. That is the harness lying about the product.
#
# CI never hits this: its PostgreSQL is a service container, new every job. On
# a laptop the choice is between refusing to run and offering a reset, and a
# harness that cannot be re-run is a harness people stop running.
#
# It is a flag rather than default behaviour because the argument for it is
# "you told me this database is disposable", and that has to be the operator's
# sentence, not the script's assumption.
if [ "$RESET_DB" = "1" ]; then
  if ! command -v psql >/dev/null; then
    echo "  ✗ --reset-db needs psql, which is not installed."
    echo "    Recreate the database container instead, or drop DB_URL's schema by hand."
    exit 1
  fi
  echo "  --reset-db: dropping and recreating schema \"public\" in the database you named"
  psql "$DB" -q -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;' || {
    echo "  ✗ reset failed"; exit 1;
  }
fi

# Refuse to run against someone else's process. Same reasoning as the m2m
# harness: a harness whose failure mode is "lies convincingly" is worse than no
# harness. Here it is worse still — the browser would log into a console served
# by a process pointed at realms this run has just deleted.
if curl -fsS -o /dev/null "$API/health" 2>/dev/null; then
  echo "  ✗ something is already listening on port $API_PORT."
  echo "    Stop it first, or set API_PORT to a free port."
  exit 1
fi

make -s -C "$ROOT" build >/dev/null || { echo "  build failed"; exit 1; }

# RATE_LIMIT_EDGE_RPS is raised deliberately.
#
# The default (10 rps / burst 20 per IP) is tuned for a human clicking. This
# run has a browser clicking AND the fixture provisioning preconditions over
# the same loopback address at machine speed, so the limiter would fire on the
# fixture and surface as an unexplained red test. The limiter itself is covered
# by internal/server/ratelimit_v1_test.go and by the m2m harness, which paces
# on purpose; re-testing it here would trade the coverage this slice is for
# against coverage that already exists.
env \
  DB_URL="$DB" \
  DB_MIGRATE_ON_BOOT=true \
  PORT="$API_PORT" \
  KEYCLOAK_URL="$KC" \
  KEYCLOAK_REALM=lw-e2e-control \
  KEYCLOAK_CLIENT_ID=lw-e2e-console \
  KEYCLOAK_CLIENT_SECRET=unused \
  KEYCLOAK_JWKS_URL="$KC/realms/lw-e2e-control/protocol/openid-connect/certs" \
  KEYCLOAK_ALLOWED_CLIENT_IDS=lw-e2e-console \
  KEYCLOAK_ADMIN_CLIENT_ID=lw-e2e-api-admin \
  KEYCLOAK_ADMIN_CLIENT_SECRET="$CTRL_SECRET" \
  SECRETS_MASTER_KEY="$(python3 -c 'import base64;print(base64.b64encode(b"slice11-browser-e2e-master-key32").decode())')" \
  ADMIN_CONSOLE_ENABLED=true \
  ADMIN_CONSOLE_CLIENT_ID=lw-e2e-console \
  DEV_PLAYGROUND_ENABLED=false \
  GIN_ACCESS_LOG_ENABLED=true \
  RATE_LIMIT_EDGE_RPS=50 \
  AUDIT_RETENTION_DAYS=90 \
  "$ROOT/bin/api" > "$LOG" 2>&1 &
API_PID=$!

# READINESS before Chromium. Opening a browser against a process that is still
# migrating produces ERR_CONNECTION_REFUSED or a 503 shell, and both hide the
# real cause behind a browser error message.
wait_for_ready "$API" 60 "$LOG" || exit 1

# ─── Known starting state ───────────────────────────────────────────────────
#
# Asked over the public API rather than in SQL, because the question is "what
# will the console show the operator on their first screen?" and the API is
# what answers that. A leftover ACTIVE workspace is not a cosmetic problem: the
# console auto-selects the first one, so a stale row decides what every journey
# opens onto.
OP_TOKEN="$(curl -s -X POST "$KC/realms/lw-e2e-control/protocol/openid-connect/token" \
  -d "grant_type=password&client_id=lw-e2e-console&username=operator&password=operator-pw" | jqp "d['access_token']")"
if [ -z "$OP_TOKEN" ]; then
  echo "  ✗ could not authenticate the fixture operator — the control realm is not usable"
  exit 1
fi

EXISTING="$(curl -s "$API/v1/workspaces" -H "Authorization: Bearer $OP_TOKEN" | jqp "len(d['workspaces'])")"
if [ -n "$EXISTING" ] && [ "$EXISTING" != "0" ]; then
  cat <<EOF

  ✗ this database already contains ${EXISTING} active workspace(s).

    The browser journeys assert on what the console shows, and the console
    auto-selects the first active workspace — so rows from an earlier run
    decide what the first screen displays, and they point at Keycloak realms
    this script has just deleted and recreated.

    Point DB_URL at an empty database, or re-run with --reset-db to drop and
    recreate the schema in the database you named.
EOF
  exit 1
fi
echo "  starting from an empty installation"

# ── 3. Toolchain ────────────────────────────────────────────────────────────
step "browser toolchain"
cd "$ROOT/tests/browser" || exit 1

if ! command -v npm >/dev/null; then
  echo "  ✗ npm not found — required for the browser suite"
  exit 1
fi

# `npm ci` installs EXACTLY the lockfile, which is what makes the toolchain
# version part of the repository rather than of whoever ran it last.
if [ ! -d node_modules ]; then
  echo "  installing pinned Playwright…"
  npm ci --silent || { echo "  npm ci failed"; exit 1; }
fi

# The browser binary is pinned by the Playwright package version, and only
# Chromium is installed — see docs/testing/BROWSER_E2E.md for why cross-browser
# is not this slice's risk.
npx --no-install playwright install chromium >/dev/null 2>&1 || {
  echo "  installing chromium…"
  npx --no-install playwright install chromium || exit 1
}

# ── 4. Drive the browser ────────────────────────────────────────────────────
step "browser journeys"

PW_EXTRA=()
[ "$HEADED" = "1" ] && PW_EXTRA+=(--headed)
# `set -u` treats an empty array expansion as unbound on bash 3.2 (the version
# macOS ships), so every array below is expanded with the +"…" guard.

# PLAYWRIGHT_NO_COPY_PROMPT=1 suppresses the "Page snapshot" section Playwright
# writes into error-context.md when a test fails.
#
# That snapshot is a full ARIA dump of the page, and an ARIA dump renders a
# textbox as `- textbox "client secret *": <value>`. A failure while the
# connection form or the one-time credential modal is on screen would therefore
# publish the secret in a plain-text artifact — the same leak the no-trace,
# no-screenshot policy exists to prevent, arriving through a third door.
#
# The error details, the failing locator and the call log all survive, which is
# what a reader actually needs; the page state is covered by
# fixtures/console-errors.js's per-test log.
export PLAYWRIGHT_NO_COPY_PROMPT=1

env \
  LW_E2E_BASE_URL="$API" \
  LW_E2E_KEYCLOAK_URL="$KC" \
  LW_E2E_CONTROL_REALM=lw-e2e-control \
  LW_E2E_CONSOLE_CLIENT_ID=lw-e2e-console \
  LW_E2E_OPERATOR_USER=operator \
  LW_E2E_OPERATOR_PASSWORD=operator-pw \
  LW_E2E_ALPHA_REALM=lw-e2e-alpha \
  LW_E2E_BRAVO_REALM=lw-e2e-bravo \
  LW_E2E_CONN_CLIENT_ID=lw-conn \
  LW_E2E_ALPHA_SECRET="$ALPHA_SECRET" \
  LW_E2E_BRAVO_SECRET="$BRAVO_SECRET" \
  LW_E2E_ALPHA_USER="$ALPHA_USER" \
  LW_E2E_BRAVO_USER="$BRAVO_USER" \
  LW_E2E_RUN_ID="$RUN_ID" \
  LW_E2E_ARTIFACT_DIR="$ARTIFACT_DIR" \
  LW_E2E_SENTINEL_DIR="$SENTINEL_DIR" \
  npx --no-install playwright test ${PW_EXTRA[@]+"${PW_EXTRA[@]}"} ${PW_ARGS[@]+"${PW_ARGS[@]}"}
PW_STATUS=$?

cd "$ROOT" || exit 1

# ── 5. Artifact isolation ───────────────────────────────────────────────────
#
# Runs whether the suite passed or failed, because a FAILING run is the one
# that produces the most artifacts and therefore the one most likely to leak.
step "artifact secret isolation"
"$ROOT/scripts/scan-artifacts.sh" "$ARTIFACT_DIR" "$SENTINEL_DIR" "$LOG"
SCAN_STATUS=$?

if [ "$PW_STATUS" != "0" ]; then
  echo
  echo "  browser journeys FAILED (exit $PW_STATUS)"
  echo "  api log tail:"
  tail -40 "$LOG"
fi

[ "$PW_STATUS" = "0" ] && [ "$SCAN_STATUS" = "0" ]
exit $?
