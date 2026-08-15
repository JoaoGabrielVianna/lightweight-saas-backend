#!/usr/bin/env bash
#
# product-acceptance.sh — install LIGHTWEIGHT the way a stranger would, and use
# it the way a stranger would.
#
# ─── What this proves that the other harnesses do not ───────────────────────
#
# scripts/m2m-harness.sh and scripts/browser-e2e.sh test boundaries. They build
# their own configuration, start the API themselves, and take the shortest path
# to the surface under test — which is right for a boundary test and wrong for
# the question this one asks:
#
#     can somebody who did not build this get from a clone to a working
#     credential, using only what the documentation tells them?
#
# So this harness is deliberately dumber. It does not construct configuration;
# it runs `./scripts/init.sh`, the command the README gives. It does not start
# a server; it runs `docker compose up`, the command the README gives. If a
# documented step does not work, this fails — which is the point, because the
# documented steps are the product.
#
# ─── What is fixture and what is product ────────────────────────────────────
#
# The line matters, so it is drawn explicitly rather than left to the reader.
#
#   FIXTURE   Two Keycloak realms with a service-account client each. These
#             stand in for identity providers an operator ALREADY RUNS.
#             LIGHTWEIGHT does not create realms and never will, so creating
#             them with Keycloak's admin API is not a shortcut around a product
#             surface — it is the precondition the product exists to consume.
#
#   PRODUCT   Everything after that. Every workspace, connection, project and
#             credential is created by an HTTP call to /v1 carrying an operator
#             token, which is exactly what the console's JavaScript does.
#             Nothing here writes to the database, calls an internal package,
#             or mints a credential by any route an operator could not use.
#
# The two-realm scenario is the product thesis in its smallest honest form: one
# installation, two workspaces, two providers, two backends that know nothing
# about either. If credential A can read realm B, the thesis is false.
#
# ─── Usage ──────────────────────────────────────────────────────────────────
#
#   ./scripts/product-acceptance.sh                  # default ports
#   LW_PORT=18080 KC_PORT=18081 ./scripts/product-acceptance.sh
#   ./scripts/product-acceptance.sh --keep           # leave the stack running
#
set -uo pipefail

REPO=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$REPO"

PROJECT=${LW_PROJECT:-lwaccept}
LW_PORT=${LW_PORT:-18080}
KC_PORT=${KC_PORT:-18081}
PG_PORT=${PG_PORT:-15432}
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

WORK=$(mktemp -d)
LW="http://localhost:$LW_PORT"
KC="http://localhost:$KC_PORT"
KC_ADMIN=admin
KC_PASS=admin

PASS=0
FAIL=0
step() { printf '\n\033[1m── %s\033[0m\n' "$*"; }
ok() { PASS=$((PASS + 1)); printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf '  \033[31m✗\033[0m %s\n' "$*"; }
die() {
	printf '\n\033[31mABORT: %s\033[0m\n' "$*"
	cleanup
	exit 1
}

# shellcheck source=lib/keycloak-fixture.sh
. "$REPO/scripts/lib/keycloak-fixture.sh"

# Both spellings exist in the wild and the Makefile already probes for them:
# the v2 plugin (`docker compose`) and the standalone binary (`docker-compose`).
if docker compose version >/dev/null 2>&1; then
	COMPOSE_BIN=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
	COMPOSE_BIN=(docker-compose)
else
	COMPOSE_BIN=()
fi

compose() {
	"${COMPOSE_BIN[@]}" -p "$PROJECT" --env-file "$WORK/.env" \
		-f "$REPO/docker-compose.yml" -f "$WORK/isolation.yml" --profile dev-idp "$@"
}

cleanup() {
	if [ "$KEEP" = "1" ]; then
		printf '\n  i --keep: stack left running as project %s (%s)\n' "$PROJECT" "$LW"
		printf '    tear down with: docker compose -p %s --env-file %s -f %s -f %s --profile dev-idp down -v\n' \
			"$PROJECT" "$WORK/.env" "$REPO/docker-compose.yml" "$WORK/isolation.yml"
		return
	fi
	# Scoped to THIS compose project by name. Never `docker rm $(docker ps -q)`:
	# a previous run of a sibling harness deleted an unrelated container that
	# way, and the blast radius of a wildcard here is somebody else's database.
	compose down -v >/dev/null 2>&1
	rm -rf "$WORK"
}
trap cleanup EXIT

# api — call the product's HTTP API as the OPERATOR. Prints the response body.
api() { # method path [body]
	local method=$1 path=$2 body=${3:-}
	if [ -n "$body" ]; then
		curl -sS -X "$method" "$LW$path" \
			-H "Authorization: Bearer $OPTOKEN" -H 'Content-Type: application/json' -d "$body"
	else
		curl -sS -X "$method" "$LW$path" -H "Authorization: Bearer $OPTOKEN"
	fi
}
# apicode — same, but prints only the HTTP status.
apicode() { # method path [body] [token]
	local method=$1 path=$2 body=${3:-} tok=${4:-$OPTOKEN}
	if [ -n "$body" ]; then
		curl -sS -o /dev/null -w '%{http_code}' -X "$method" "$LW$path" \
			-H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d "$body"
	else
		curl -sS -o /dev/null -w '%{http_code}' -X "$method" "$LW$path" -H "Authorization: Bearer $tok"
	fi
}

for tool in docker curl python3 go; do
	command -v $tool >/dev/null || die "$tool is required"
done
[ ${#COMPOSE_BIN[@]} -gt 0 ] || die "docker compose (v2 plugin or standalone docker-compose) is required"

for p in $LW_PORT $KC_PORT $PG_PORT; do
	if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
		die "port $p is already in use. Set LW_PORT / KC_PORT / PG_PORT to free ports."
	fi
done

# Can Docker actually bind-mount from this checkout?
#
# Docker Desktop shares a configured list of host paths. A bind mount from a
# path outside that list does not fail — it silently mounts an EMPTY directory.
# The evaluation Keycloak takes its realm import that way, so the whole failure
# surfaces two minutes later as the API retrying JWKS against a realm that does
# not exist, with a 404 and no hint that a mount was involved.
#
# Cheap to detect, so detect it: mount the realm export and look for it.
if ! docker run --rm -v "$REPO/deploy/keycloak:/m:ro" alpine \
	test -f /m/realm-export.json >/dev/null 2>&1; then
	die "Docker cannot bind-mount $REPO/deploy/keycloak — it appears empty inside a container.
    The evaluation Keycloak imports its realm from there, so nothing will work.
    On Docker Desktop, add this path under Settings > Resources > File sharing,
    or move the checkout somewhere already shared (a path under \$HOME usually is)."
fi

# ─────────────────────────────────────────────────────────────────────────────
step "1. Install, using only the documented commands"

# The documented first step, run verbatim against a directory that has nothing
# in it but the two files a clone would provide.
mkdir -p "$WORK/scripts"
cp "$REPO/.env.example" "$WORK/.env.example"
cp "$REPO/scripts/init.sh" "$WORK/scripts/init.sh"
(cd "$WORK" && ./scripts/init.sh >"$WORK/init.log" 2>&1) || die "scripts/init.sh failed: $(cat "$WORK/init.log")"

grep -q '^SECRETS_KEYRING=1:.\+' "$WORK/.env" && ok "init.sh generated a secrets keyring" ||
	bad "init.sh left SECRETS_KEYRING empty — connections would not be mounted"
[ "$(stat -f '%Lp' "$WORK/.env" 2>/dev/null || stat -c '%a' "$WORK/.env")" = "600" ] &&
	ok ".env is not world-readable" || bad ".env permissions are not 600"

# Relocate the published ports. Product configuration, not a patch to the
# compose file: an installation whose host already uses 8080 must be able to do
# exactly this.
setvar() {
	awk -v n="$1" -v v="$2" '$0 ~ "^" n "=" && !d { print n "=" v; d=1; next } { print }' \
		"$WORK/.env" >"$WORK/.env.t" && mv "$WORK/.env.t" "$WORK/.env"
}
setvar API_HOST_PORT "$LW_PORT"
setvar POSTGRES_HOST_PORT "$PG_PORT"
setvar KC_HOST_PORT "$KC_PORT"
setvar KEYCLOAK_URL "http://localhost:$KC_PORT"

# The evaluation stack's two remaining services publish fixed ports, which is
# fine on a VPS and not fine on a laptop already running one. Harness isolation
# only — nothing here changes how the product is configured.
cat >"$WORK/isolation.yml" <<YAML
services:
  postgres: { container_name: ${PROJECT}-postgres }
  api: { container_name: ${PROJECT}-api }
  keycloak: { container_name: ${PROJECT}-keycloak }
  keycloak-postgres:
    container_name: ${PROJECT}-kcdb
    ports: !override ["$((PG_PORT + 1)):5432"]
  mailpit:
    container_name: ${PROJECT}-mailpit
    ports: !override ["$((LW_PORT + 945)):8025", "$((LW_PORT + 2945)):1025"]
YAML

T0=$(date +%s)
compose up -d --build >"$WORK/up.log" 2>&1 || die "docker compose up failed: $(tail -20 "$WORK/up.log")"

# The documented readiness check, not "look at the logs".
READY=0
for _ in $(seq 1 90); do
	if curl -fsS --max-time 2 "$LW/health/ready" >/dev/null 2>&1; then
		READY=1
		break
	fi
	sleep 2
done
[ "$READY" = 1 ] || die "never became ready. api log:
$(docker logs "${PROJECT}-api" 2>&1 | tail -25)"
T_READY=$(($(date +%s) - T0))
ok "ready in ${T_READY}s: $(curl -sS "$LW/health/ready")"

# A successful first boot must not look like a failed one.
# At most one. The bundled Keycloak imports a realm on its very first boot and
# can take minutes; the API's issuer retry is bounded on purpose, so one
# restart while that finishes is convergence. Eight was the old behaviour and
# is what this guards against — a first boot that succeeds must not read like a
# crash loop.
FATALS=$(docker logs "${PROJECT}-api" 2>&1 | grep -c 'FATAL' || true)
[ "${FATALS:-0}" -le 1 ] && ok "successful first boot printed $FATALS FATAL lines (<=1)" ||
	bad "$FATALS FATAL lines during a boot that succeeded — reads as broken"

# Migrations are part of boot, not a step the operator runs.
#
# Counted rather than `grep -q`: under `pipefail`, grep -q exits on the first
# match, docker logs takes SIGPIPE, and the pipeline reports failure for a
# pattern that WAS found. That cost twenty minutes of looking at the wrong
# thing, and the same trap is why the FATAL count above is written this way.
MIGLINES=$(docker logs "${PROJECT}-api" 2>&1 | grep -cE 'migrations applied|schema already at the latest' || true)
[ "${MIGLINES:-0}" -ge 1 ] && ok "schema migrated automatically at boot" ||
	bad "no migration line in the boot log"

# ─────────────────────────────────────────────────────────────────────────────
step "2. Fixture: two Keycloak realms that already exist"

kc_authenticate >/dev/null 2>&1 || die "cannot reach the fixture Keycloak at $KC"
for r in acme-corp globex-inc; do
	make_realm "$r"
done
SECRET_A=$(make_sa_client acme-corp lw-connector realm-admin)
SECRET_B=$(make_sa_client globex-inc lw-connector realm-admin)
[ -n "$SECRET_A" ] && [ -n "$SECRET_B" ] && ok "two provider realms exist, each with a service-account client" ||
	die "fixture realm setup failed"

# ─────────────────────────────────────────────────────────────────────────────
step "3. Operator signs in"

# The installation realm — where LIGHTWEIGHT says who may administer it. This
# is an ordinary OIDC password grant against the realm named in KEYCLOAK_REALM,
# the same token the console holds after a PKCE login.
OPTOKEN=$(curl -sS -X POST "$KC/realms/saas/protocol/openid-connect/token" \
	-d 'client_id=saas-backend' -d 'client_secret=saas-backend-secret' \
	-d 'grant_type=password' -d 'username=adminuser' -d 'password=password' |
	python3 -c 'import sys,json;print(json.load(sys.stdin).get("access_token",""))')
[ -n "$OPTOKEN" ] || die "could not obtain an operator token from the installation realm"
ok "operator authenticated against the installation realm"

[ "$(apicode GET /v1/workspaces)" = "200" ] && ok "operator token is accepted by /v1" ||
	bad "operator token rejected by /v1"

# ─────────────────────────────────────────────────────────────────────────────
# onboard — the entire operator journey for one tenant, through the product's
# own API. Sets WS_ID and CRED for the caller.
onboard() { # label realm client-secret
	local label=$1 realm=$2 secret=$3 body

	body=$(api POST /v1/workspaces "{\"name\":\"$label\"}")
	WS_ID=$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null)
	[ -n "$WS_ID" ] || die "create workspace $label: $body"
	ok "$label: workspace $WS_ID"

	body=$(api POST "/v1/workspaces/$WS_ID/connections" \
		"{\"name\":\"$realm idp\",\"provider\":\"keycloak\",\"base_url\":\"http://keycloak:8080\",\"realm\":\"$realm\",\"client_id\":\"lw-connector\",\"client_secret\":\"$secret\"}")
	CONN_ID=$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null)
	[ -n "$CONN_ID" ] || die "create connection for $label: $body"
	ok "$label: connection $CONN_ID -> realm $realm"

	body=$(api POST "/v1/workspaces/$WS_ID/connections/$CONN_ID/verify")
	local health mode
	health=$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("connection",{}).get("health",""))' 2>/dev/null)
	mode=$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("connection",{}).get("access_mode",""))' 2>/dev/null)
	[ "$health" = "healthy" ] && ok "$label: verify -> healthy, access_mode=$mode" ||
		die "$label: verify did not report healthy: $body"

	[ "$(apicode POST "/v1/workspaces/$WS_ID/connections/$CONN_ID/activate")" = "200" ] &&
		ok "$label: connection activated" || die "$label: activate failed"

	body=$(api POST "/v1/workspaces/$WS_ID/projects" "{\"name\":\"$label backend\"}")
	local prj
	prj=$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null)
	[ -n "$prj" ] || die "create project for $label: $body"
	ok "$label: project $prj"

	body=$(api POST "/v1/workspaces/$WS_ID/projects/$prj/credentials" \
		'{"label":"acceptance","scopes":["users:read","users:write","roles:read"]}')
	CRED=$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("secret",""))' 2>/dev/null)
	case "$CRED" in
	lw_sk_*) ok "$label: credential issued (lw_sk_…, shown once)" ;;
	*) die "$label: no lw_sk_ credential in response: $body" ;;
	esac

	# The plaintext must not be retrievable afterwards. A product that can show
	# it twice has a different security story than the one it advertises.
	if api GET "/v1/workspaces/$WS_ID/projects/$prj/credentials" | grep -q "$CRED"; then
		bad "$label: the credential list replays the plaintext secret"
	else
		ok "$label: listing credentials never returns the plaintext again"
	fi
}

step "4. Workspace A — acme-corp"
onboard "Acme" acme-corp "$SECRET_A"
WS_A=$WS_ID
CRED_A=$CRED

step "5. Workspace B — globex-inc"
onboard "Globex" globex-inc "$SECRET_B"
WS_B=$WS_ID
CRED_B=$CRED

# ─────────────────────────────────────────────────────────────────────────────
step "6. Two external backends, through the Go SDK"

# The consumer is a module OUTSIDE this repository. It is given three
# environment variables and nothing else: no database, no Keycloak address, no
# realm, no provider secret, no operator token. If it needs a fourth, the
# product's central claim is wrong.
mkdir -p "$WORK/consumer"
cat >"$WORK/consumer/go.mod" <<EOF
module example.com/acceptance-consumer

go 1.25

require github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go v0.0.0

replace github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go => $REPO/sdk/go
EOF

cat >"$WORK/consumer/main.go" <<'EOF'
// An external backend. It imports the SDK and knows three environment
// variables. Everything it learns about identity, it learns through
// LIGHTWEIGHT.
package main

import (
	"context"
	"fmt"
	"os"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

func main() {
	ctx := context.Background()
	client, err := lightweight.NewClientFromEnv()
	if err != nil {
		fmt.Println("ERR config:", err)
		os.Exit(1)
	}

	email := os.Args[1]

	created, err := client.Users.Create(ctx, lightweight.CreateUserRequest{
		Email:             email,
		FirstName:         "Acceptance",
		LastName:          "Probe",
		TemporaryPassword: "acceptance-probe-8chars",
	})
	if err != nil {
		fmt.Println("ERR create:", err)
		os.Exit(1)
	}
	fmt.Println("CREATED", created.ID, created.Username)

	got, err := client.Users.Get(ctx, created.ID)
	if err != nil {
		fmt.Println("ERR get:", err)
		os.Exit(1)
	}
	fmt.Println("READ", got.Username)

	page, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 50})
	if err != nil {
		fmt.Println("ERR list:", err)
		os.Exit(1)
	}
	fmt.Println("LISTED", len(page.Users))

	roles, err := client.Roles.List(ctx)
	if err != nil {
		fmt.Println("ERR roles:", err)
		os.Exit(1)
	}
	fmt.Println("ROLES", len(roles))
}
EOF

(cd "$WORK/consumer" && go mod tidy >/dev/null 2>&1 && go build -o consumer . 2>"$WORK/build.log") ||
	die "the external consumer did not compile: $(cat "$WORK/build.log")"
ok "an external module compiles against the SDK alone"

run_consumer() { # workspace-id credential email
	(cd "$WORK/consumer" && env -i PATH="$PATH" HOME="$HOME" \
		LIGHTWEIGHT_URL="$LW" LIGHTWEIGHT_WORKSPACE_ID="$1" LIGHTWEIGHT_API_KEY="$2" \
		./consumer "$3" 2>&1)
}

USER_A=acme-alice@example.test
OUT_A=$(run_consumer "$WS_A" "$CRED_A" "$USER_A")
printf '%s' "$OUT_A" | grep -q '^CREATED' && printf '%s' "$OUT_A" | grep -q '^ROLES' &&
	ok "consumer A: created, read, listed users and roles" || bad "consumer A failed: $OUT_A"

USER_B=globex-bob@example.test
OUT_B=$(run_consumer "$WS_B" "$CRED_B" "$USER_B")
printf '%s' "$OUT_B" | grep -q '^CREATED' && printf '%s' "$OUT_B" | grep -q '^ROLES' &&
	ok "consumer B: created, read, listed users and roles" || bad "consumer B failed: $OUT_B"

# ─────────────────────────────────────────────────────────────────────────────
step "7. Isolation, confirmed at the provider"

# Read Keycloak directly. Allowed — and necessary — because the claim under
# test is about where the write LANDED, and only the provider can answer that.
in_realm() { # realm username -> "yes"/"no"
	local n enc
	enc=$(printf '%s' "$2" | sed 's/@/%40/g')
	n=$(kcg "/admin/realms/$1/users?username=$enc&exact=true" | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))' 2>/dev/null)
	[ "${n:-0}" -gt 0 ] && echo yes || echo no
}

[ "$(in_realm acme-corp "$USER_A")" = yes ] && ok "$USER_A exists in realm acme-corp" ||
	bad "$USER_A is missing from acme-corp"
[ "$(in_realm globex-inc "$USER_B")" = yes ] && ok "$USER_B exists in realm globex-inc" ||
	bad "$USER_B is missing from globex-inc"
[ "$(in_realm globex-inc "$USER_A")" = no ] && ok "$USER_A does NOT leak into globex-inc" ||
	bad "acme-alice leaked into the other tenant's realm"
[ "$(in_realm acme-corp "$USER_B")" = no ] && ok "$USER_B does NOT leak into acme-corp" ||
	bad "globex-bob leaked into the other tenant's realm"

# ─────────────────────────────────────────────────────────────────────────────
step "8. Negative: credential A against workspace B"

CODE=$(curl -sS -o "$WORK/cross.json" -w '%{http_code}' \
	-H "Authorization: Bearer $CRED_A" "$LW/v1/workspaces/$WS_B/users?max=1")
if [ "$CODE" = "403" ] || [ "$CODE" = "404" ]; then
	ok "credential A against workspace B -> $CODE $(python3 -c 'import sys,json;d=json.load(open(sys.argv[1]));print(d.get("code") or d.get("error") or d)' "$WORK/cross.json" 2>/dev/null)"
else
	bad "credential A reached workspace B: HTTP $CODE $(cat "$WORK/cross.json")"
fi

# A revoked-shaped credential must not authenticate either.
CODE=$(curl -sS -o /dev/null -w '%{http_code}' \
	-H "Authorization: Bearer lw_sk_0000000000000000_0000000000000000000000000000000000000000000" \
	"$LW/v1/workspaces/$WS_A/users?max=1")
[ "$CODE" = "401" ] && ok "a forged credential is rejected (401)" || bad "forged credential got HTTP $CODE"

# Control-plane operations must stay operator-only.
CODE=$(apicode GET /v1/workspaces "" "$CRED_A")
[ "$CODE" = "403" ] && ok "a project credential cannot list workspaces (403 operator_only)" ||
	bad "project credential reached the control plane: HTTP $CODE"

# ─────────────────────────────────────────────────────────────────────────────
step "9. No provider secret reaches a consumer"

# The consumer's whole environment and its build output must not contain the
# realm's client secret. If it does, the indirection the product sells is
# cosmetic.
if printf '%s' "$OUT_A$OUT_B" | grep -q "$SECRET_A\|$SECRET_B"; then
	bad "a provider client secret appeared in consumer output"
else
	ok "no provider secret appears anywhere in consumer output"
fi
if docker logs "${PROJECT}-api" 2>&1 | grep -q "$SECRET_A\|$SECRET_B"; then
	bad "a provider client secret was written to the API log"
else
	ok "no provider secret was written to the API log"
fi

# ─────────────────────────────────────────────────────────────────────────────
printf '\n\033[1m── result ──\033[0m\n'
printf '  %s passed, %s failed\n' "$PASS" "$FAIL"
printf '  time from `docker compose up` to ready: %ss\n' "$T_READY"
[ "$FAIL" = 0 ] || exit 1
printf '\n  \033[32mProduct acceptance passes.\033[0m One installation, two workspaces,\n'
printf '  two Keycloak realms, two external backends holding three variables each.\n\n'
