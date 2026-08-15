#!/usr/bin/env bash
# secrets-rotation-check.sh — drive a real master-key rotation through the real
# CLI, and prove nothing it produced contains a secret.
#
# ─── Why a shell harness when the Go tests already cover rotation ───────────
#
# internal/connection's integration suite proves the ROWS rotate correctly, and
# proves RunSecretsCLI's output carries no secret. What it cannot exercise is
# the part an operator actually touches:
#
#   * the compiled cmd/secrets binary, not the function behind it;
#   * SECRETS_KEYRING and SECRETS_KEY_CURRENT arriving as environment
#     variables, including the legacy SECRETS_MASTER_KEY bridge;
#   * the exit codes a deploy script branches on;
#   * everything the run leaves on disk — stdout, stderr, and the rows.
#
# The scan itself is scripts/scan-artifacts.sh, unchanged and shared with the
# browser suite. That reuse is deliberate: a second secret-scanner would be a
# second vocabulary of what counts as a leak, and the two would drift.
#
# Usage:
#   DB_URL=postgres://…  ./scripts/secrets-rotation-check.sh
#
# > **Point DB_URL at a throwaway database.** This creates and drops a schema.
set -uo pipefail

cd "$(dirname "$0")/.."

DB="${DB_URL:?DB_URL required — point it at a THROWAWAY database}"
SCHEMA="secrets_rotation_check"

WORK="$(mktemp -d)"
ARTIFACTS="$WORK/artifacts"
SENTINELS="$WORK/sentinels"     # never scanned; it is the list of what to look FOR
mkdir -p "$ARTIFACTS" "$SENTINELS"

cleanup() {
  psql_exec "DROP SCHEMA IF EXISTS $SCHEMA CASCADE" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}

psql_exec() { psql "$DB" -v ON_ERROR_STOP=1 -tAc "$1"; }

command -v psql >/dev/null || { echo "  ✗ psql required"; exit 2; }
trap cleanup EXIT

# ── Key material, generated here and never written to an artifact ───────────
KEY_V1="$(openssl rand -base64 32)"
KEY_V2="$(openssl rand -base64 32)"

# The provider secrets the connections will hold. Unique sentinels so a hit is
# unambiguous and cannot be a coincidence.
SECRET_A="SENTINEL-PROVIDER-A-$(openssl rand -hex 8)"
SECRET_B="SENTINEL-PROVIDER-B-$(openssl rand -hex 8)"

# Everything the scan must not find, one per line.
{
  echo "$KEY_V1"
  echo "$KEY_V2"
  echo "$SECRET_A"
  echo "$SECRET_B"
} > "$SENTINELS/secrets.txt"

echo "── preparing schema $SCHEMA ────────────────────────────────────────"
psql_exec "DROP SCHEMA IF EXISTS $SCHEMA CASCADE" >/dev/null
psql_exec "CREATE SCHEMA $SCHEMA" >/dev/null

# A DSN scoped to the private schema, so this cannot touch anything else in the
# database it was pointed at.
case "$DB" in
  *\?*) SCOPED="$DB&search_path=$SCHEMA" ;;
  *)    SCOPED="$DB?search_path=$SCHEMA" ;;
esac

DB_URL="$SCOPED" go run ./cmd/migrate up >/dev/null 2>&1 || {
  echo "  ✗ migrations failed"; exit 1; }

# Built, not `go run`. `go run` collapses a program's exit code 2 into its own
# exit 1, and the three-way exit contract is exactly what this harness checks.
SECRETS_BIN="$WORK/secrets"
go build -trimpath -o "$SECRETS_BIN" ./cmd/secrets || {
  echo "  ✗ building cmd/secrets failed"; exit 1; }

# ── Seed two connections under the LEGACY variable ──────────────────────────
#
# Deliberately the legacy spelling: this is what an installation upgrading into
# this slice actually has, and the whole rotation story starts from it.
echo "── seeding two connections under SECRETS_MASTER_KEY (legacy) ───────"
DB_URL="$SCOPED" SECRETS_MASTER_KEY="$KEY_V1" LW_SEED_CONNECTIONS="$SECRET_A,$SECRET_B" \
  go test -tags=integration -count=1 -run TestSeedConnectionsForRotationCheck \
  ./internal/connection/ \
  > "$ARTIFACTS/seed.stdout" 2> "$ARTIFACTS/seed.stderr"
SEED_CODE=$?
if [ "$SEED_CODE" != "0" ]; then
  echo "  ✗ seeding failed (exit $SEED_CODE); see $ARTIFACTS/seed.stderr" >&2
  cat "$ARTIFACTS/seed.stderr" >&2
  exit 1
fi

versions_in_db() {
  psql_exec "SELECT string_agg(secret_key_version::text, ',' ORDER BY secret_key_version) FROM $SCHEMA.connections"
}

echo "  key versions in the database: $(versions_in_db)"
[ "$(versions_in_db)" = "1,1" ] || {
  echo "  ✗ the legacy key did not seal rows as version 1" >&2; exit 1; }

FAILED=0
step() { # label expected-exit command...
  local label="$1" want="$2"; shift 2
  local slug; slug="$(echo "$label" | tr ' /' '__')"
  "$@" > "$ARTIFACTS/$slug.stdout" 2> "$ARTIFACTS/$slug.stderr"
  local code=$?
  if [ "$code" != "$want" ]; then
    echo "  ✗ $label exited $code, want $want" >&2
    sed 's/^/      /' "$ARTIFACTS/$slug.stderr" >&2
    FAILED=1
  else
    echo "  + $label exited $code"
  fi
}

# ── Before rotation: status must report v1 in use and nothing removable ─────
echo "── status, mid-migration (v1 legacy, v2 current) ───────────────────"
export SECRETS_KEYRING="1:$KEY_V1,2:$KEY_V2"
export SECRETS_KEY_CURRENT=2
unset SECRETS_MASTER_KEY

step "status before rotation" 0 env DB_URL="$SCOPED" "$SECRETS_BIN" status
grep -q "Safe to remove:    (none)" "$ARTIFACTS/status_before_rotation.stdout" || {
  echo "  ✗ v1 was reported removable while two rows still need it" >&2; FAILED=1; }

step "dry run" 0 env DB_URL="$SCOPED" "$SECRETS_BIN" rotate --dry-run
grep -q "Would rotate:      2" "$ARTIFACTS/dry_run.stdout" || {
  echo "  ✗ the dry run did not report two rows needing rotation" >&2; FAILED=1; }
[ "$(versions_in_db)" = "1,1" ] || {
  echo "  ✗ the dry run wrote to the database" >&2; FAILED=1; }

# ── Rotate ──────────────────────────────────────────────────────────────────
echo "── rotate ──────────────────────────────────────────────────────────"
step "rotate" 0 env DB_URL="$SCOPED" "$SECRETS_BIN" rotate
[ "$(versions_in_db)" = "2,2" ] || {
  echo "  ✗ rows are $(versions_in_db) after rotation, want 2,2" >&2; FAILED=1; }

step "rotate again (idempotent)" 0 env DB_URL="$SCOPED" "$SECRETS_BIN" rotate
grep -q "rotated:           0" "$ARTIFACTS/rotate_again_(idempotent).stdout" 2>/dev/null ||
  grep -q "rotated:           0" "$ARTIFACTS"/rotate_again*.stdout || {
    echo "  ✗ a second rotation did work; it is not idempotent" >&2; FAILED=1; }

step "status after rotation" 0 env DB_URL="$SCOPED" "$SECRETS_BIN" status
grep -q "v1" "$ARTIFACTS/status_after_rotation.stdout" &&
  grep -q "Safe to remove:" "$ARTIFACTS/status_after_rotation.stdout" || {
    echo "  ✗ status does not report v1 as safe to remove" >&2; FAILED=1; }

# ── The old key is destroyed ────────────────────────────────────────────────
echo "── with v1 removed entirely ────────────────────────────────────────"
export SECRETS_KEYRING="2:$KEY_V2"
export SECRETS_KEY_CURRENT=2

step "status without v1" 0 env DB_URL="$SCOPED" "$SECRETS_BIN" status
step "rotate without v1" 0 env DB_URL="$SCOPED" "$SECRETS_BIN" rotate

# ── Refusals ────────────────────────────────────────────────────────────────
echo "── refusals ────────────────────────────────────────────────────────"
step "both variables set" 2 \
  env DB_URL="$SCOPED" SECRETS_MASTER_KEY="$KEY_V1" "$SECRETS_BIN" status
step "current version absent from the ring" 2 \
  env DB_URL="$SCOPED" SECRETS_KEYRING="2:$KEY_V2" SECRETS_KEY_CURRENT=9 \
  "$SECRETS_BIN" status

# A row needing a key nobody holds: put v1 back on the rows, then take it away.
export SECRETS_KEYRING="1:$KEY_V1,2:$KEY_V2"
export SECRETS_KEY_CURRENT=1
env DB_URL="$SCOPED" "$SECRETS_BIN" rotate >/dev/null 2>&1
export SECRETS_KEYRING="2:$KEY_V2"
export SECRETS_KEY_CURRENT=2
step "a persisted version is missing" 1 env DB_URL="$SCOPED" "$SECRETS_BIN" status
step "rotation blocked by a missing key" 1 env DB_URL="$SCOPED" "$SECRETS_BIN" rotate

# ── The database's own bytes ────────────────────────────────────────────────
#
# Dumped into the artifact directory so the scan covers what is actually
# STORED, not only what was printed. If a plaintext ever reached a column, this
# is where it shows up.
psql "$DB" -tAc "SELECT id, name, encode(secret_ciphertext,'escape'), encode(secret_nonce,'escape'), secret_key_version, secret_alg FROM $SCHEMA.connections" \
  > "$ARTIFACTS/connections.dump" 2>/dev/null

echo
echo "── secret isolation scan ───────────────────────────────────────────"
./scripts/scan-artifacts.sh "$ARTIFACTS" "$SENTINELS" || FAILED=1

echo
if [ "$FAILED" != "0" ]; then
  echo "  ✗ secrets rotation check FAILED"
  exit 1
fi
echo "  ✓ secrets rotation check passed"
exit 0
