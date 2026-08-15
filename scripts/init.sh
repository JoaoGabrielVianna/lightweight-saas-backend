#!/bin/sh
#
# init.sh — turn a fresh clone into a configured installation.
#
# Copies .env.example to .env, generates the one secret that has no safe
# default, and prints the next command. That is the whole job.
#
#   ./scripts/init.sh
#   ./scripts/init.sh --keycloak-url https://sso.example.com \
#                     --realm lightweight --console-client-id lightweight-console
#
# ─── Why this is not `make init` ────────────────────────────────────────────
#
# `make init` runs cmd/bootstrap: it prompts, and it regenerates project.json,
# .env.example and the Keycloak realm export from answers. That is a tool for
# someone FORKING this project and changing what it generates. It needs a Go
# toolchain and a terminal.
#
# Installing does not. A VPS has Docker and git, an IaC run has neither a TTY
# nor a Go compiler, and neither should have to answer questions about realm
# exports to get a server running. So this is POSIX sh, reads no input, and
# calls nothing that is not already required to run the stack.
#
# ─── Why it refuses rather than overwrites ──────────────────────────────────
#
# Re-running it on a configured installation would mint a NEW secrets keyring.
# Every provider credential in the database is sealed under the OLD one, and
# nothing would report the loss until a workspace tried to reach its realm and
# could not be decrypted. There is no recovery from that except restoring the
# previous key.
#
# So an existing .env is left exactly as it is, and this exits 0 — re-running
# is a no-op, which is what an idempotent provisioning step needs.
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
ENV_FILE="$ROOT/.env"
EXAMPLE="$ROOT/.env.example"

KEYCLOAK_URL=""
REALM=""
CONSOLE_CLIENT_ID=""

usage() {
	cat <<'USAGE'
init.sh — create .env and generate the secrets keyring.

  --keycloak-url URL        issuer operators log in against, as a BROWSER
                            reaches it. Setting it also clears the two
                            container-internal overrides, which exist only for
                            the bundled evaluation Keycloak.
  --realm NAME              installation realm (default: from .env.example)
  --console-client-id ID    public PKCE client the console logs in with
  -h, --help                this

With no flags the defaults describe the bundled evaluation stack, and the
installation is ready for:  docker compose --profile dev-idp up -d
USAGE
}

while [ $# -gt 0 ]; do
	case "$1" in
	--keycloak-url) KEYCLOAK_URL="${2:?--keycloak-url needs a value}"; shift 2 ;;
	--realm) REALM="${2:?--realm needs a value}"; shift 2 ;;
	--console-client-id) CONSOLE_CLIENT_ID="${2:?--console-client-id needs a value}"; shift 2 ;;
	-h | --help) usage; exit 0 ;;
	*) echo "init.sh: unknown argument: $1" >&2; usage >&2; exit 2 ;;
	esac
done

if [ -f "$ENV_FILE" ]; then
	echo "  i .env already exists — leaving it untouched."
	echo "    Nothing was changed. Delete it yourself if you mean to start over,"
	echo "    but read this first: a new file gets a NEW secrets keyring, and every"
	echo "    provider credential already stored is sealed under the current one."
	exit 0
fi

[ -f "$EXAMPLE" ] || { echo "  - $EXAMPLE not found; is this a LIGHTWEIGHT checkout?" >&2; exit 1; }

# genkey — 32 random bytes, base64. openssl if it is here (it usually is, and
# the docs name it), /dev/urandom otherwise so a minimal container still works.
genkey() {
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -base64 32
	elif [ -r /dev/urandom ]; then
		dd if=/dev/urandom bs=32 count=1 2>/dev/null | base64 | tr -d '\n'
	else
		echo "  - no openssl and no /dev/urandom: cannot generate a key safely." >&2
		echo "    Refusing to invent one — a predictable key seals nothing." >&2
		exit 1
	fi
}

# setvar — replace a whole `NAME=...` line in place. The file is one this repo
# generates, so every variable appears exactly once at the start of a line.
setvar() {
	name=$1
	value=$2
	awk -v n="$name" -v v="$value" '
		$0 ~ "^" n "=" && !done { print n "=" v; done=1; next }
		{ print }
	' "$ENV_FILE" >"$ENV_FILE.tmp" && mv "$ENV_FILE.tmp" "$ENV_FILE"
}

umask 077 # the file is about to hold a key; do not create it world-readable
cp "$EXAMPLE" "$ENV_FILE"

setvar SECRETS_KEYRING "1:$(genkey)"
setvar SECRETS_KEY_CURRENT "1"
# The database is published on a host port, so the example's postgres/postgres
# is a real exposure and not a placeholder. Costs the operator nothing: compose
# builds the API's DB_URL from these same three values.
PGPASS=$(genkey | tr -d '/+=' | cut -c1-24)
setvar POSTGRES_PASSWORD "$PGPASS"
setvar DB_URL "postgres://postgres:$PGPASS@localhost:5432/lightweight_saas_backend_db?sslmode=disable"

MODE="evaluation"
if [ -n "$KEYCLOAK_URL" ]; then
	MODE="self-hosted"
	setvar KEYCLOAK_URL "$KEYCLOAK_URL"
	# Both exist only because the bundled Keycloak answers on one address for
	# browsers and another on the compose network. Against a real issuer there
	# is one address, and empty means "derive it from KEYCLOAK_URL".
	setvar KEYCLOAK_JWKS_URL ""
	setvar KEYCLOAK_ADMIN_BASE_URL ""
	setvar DEV_PLAYGROUND_ENABLED "false"

	# The legacy /admin/* identity surface, switched OFF for a self-host.
	#
	# It is not merely unused when left configured — it breaks the installation.
	# These two variables also construct the live-admin checker, which re-asks
	# the provider whether an operator still holds the admin role, and that
	# check is deliberately FAIL-CLOSED: when it cannot get an answer it returns
	# 503 rather than trusting the token's claim.
	#
	# So an installation carrying the evaluation client id into a realm that has
	# no such client answers 503 to every /v1 request, from an operator whose
	# token is perfectly valid. Ready, healthy, and refusing everything.
	#
	# Blank means the checker is not built and /admin/* is not mounted, which
	# matches the realm setup KEYCLOAK_SETUP.md §0.1 asks for. An operator who
	# wants the live re-check adds a service-account client and sets these two
	# back; §0.1 says how, and what the window costs without it.
	setvar KEYCLOAK_ADMIN_CLIENT_ID ""
	setvar KEYCLOAK_ADMIN_CLIENT_SECRET ""
fi
[ -n "$REALM" ] && setvar KEYCLOAK_REALM "$REALM"

# Naming the console's client has to update all THREE variables that mention a
# client, or the installation rejects the very tokens it was just told to
# expect.
#
# Only ADMIN_CONSOLE_CLIENT_ID used to change. The other two kept their
# evaluation values, so KEYCLOAK_ALLOWED_CLIENT_IDS still read
# `saas-backend,saas-dev-playground` — an allow-list matched against the token's
# `azp` claim. A self-hosted operator would create their client, sign in, and
# get 401 on every request from a token that was completely valid, with the
# reason only visible in the server log. The documented self-host path did not
# work.
#
# They are one decision, so they are set together:
#
#   KEYCLOAK_CLIENT_ID            the client operators authenticate with
#   ADMIN_CONSOLE_CLIENT_ID       the client the console starts PKCE with
#   KEYCLOAK_ALLOWED_CLIENT_IDS   which `azp` values are accepted at all
#
# Narrowed to exactly this client rather than blanked. Empty accepts every
# client in the realm, which is a larger grant than "let my console in".
if [ -n "$CONSOLE_CLIENT_ID" ]; then
	setvar ADMIN_CONSOLE_CLIENT_ID "$CONSOLE_CLIENT_ID"
	setvar KEYCLOAK_CLIENT_ID "$CONSOLE_CLIENT_ID"
	setvar KEYCLOAK_ALLOWED_CLIENT_IDS "$CONSOLE_CLIENT_ID"
	# A public PKCE client has no secret, and leaving the evaluation one behind
	# would be a stale credential in a production .env.
	setvar KEYCLOAK_CLIENT_SECRET ""
fi

chmod 600 "$ENV_FILE"

echo "  + wrote .env  (mode: $MODE, permissions 600)"
echo "  + generated SECRETS_KEYRING version 1 and a database password"
echo
echo "    BACK UP THE KEYRING, separately from the database. A dump restored"
echo "    without it contains provider credentials nobody can decrypt."
echo

if [ "$MODE" = "evaluation" ]; then
	cat <<'NEXT'
  Next — evaluation, with a throwaway Keycloak included:

      docker compose --profile dev-idp up -d
      curl -fsS localhost:8080/health/ready

  Then open http://localhost:8080/admin and sign in as adminuser / password.

  Self-hosting against a Keycloak you already run instead? Delete .env and:

      ./scripts/init.sh --keycloak-url https://sso.example.com \
                        --realm lightweight --console-client-id lightweight-console
NEXT
else
	cat <<'NEXT'
  Next — set up the installation realm in your Keycloak first. It needs one
  public PKCE client and one user holding the realm role `admin`:

      docs/getting-started/KEYCLOAK_SETUP.md §0.1

  Then:

      docker compose up -d
      curl -fsS localhost:8080/health/ready
NEXT
fi
