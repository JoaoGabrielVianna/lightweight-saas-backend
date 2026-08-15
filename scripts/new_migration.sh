#!/usr/bin/env bash
# new_migration.sh — scaffold an up/down migration pair.
#
# Why a script and not `migrate create`: the golang-migrate CLI is a separate
# binary nobody has installed, and its default timestamp versioning produces
# names like 20260728124300_x.up.sql. This repo uses dense six-digit sequence
# numbers instead, because "version 3" is something an operator can hold in
# their head when reading `make migrate-version`, and a timestamp is not.
#
# Usage:  scripts/new_migration.sh add_workspaces
set -euo pipefail

NAME="${1:-}"
DIR="internal/database/migrations"

if [ -z "$NAME" ]; then
  echo "✗ usage: scripts/new_migration.sh <name>   (e.g. add_workspaces)" >&2
  exit 1
fi

# The naming regex is enforced by TestEmbeddedMigrations_Naming; reject bad
# input here rather than letting a silently-ignored file reach the tree.
if ! printf '%s' "$NAME" | grep -Eq '^[a-z0-9_]+$'; then
  echo "✗ name must be lowercase letters, digits and underscores: got '$NAME'" >&2
  exit 1
fi

if [ ! -d "$DIR" ]; then
  echo "✗ $DIR not found — run this from the repository root" >&2
  exit 1
fi

LAST=$(find "$DIR" -name '*.up.sql' -exec basename {} \; \
  | cut -d_ -f1 | sort -n | tail -1)
NEXT=$(printf '%06d' $((10#${LAST:-0} + 1)))

UP="$DIR/${NEXT}_${NAME}.up.sql"
DOWN="$DIR/${NEXT}_${NAME}.down.sql"

if [ -e "$UP" ] || [ -e "$DOWN" ]; then
  echo "✗ $UP or $DOWN already exists" >&2
  exit 1
fi

cat > "$UP" <<EOF
-- ${NEXT}_${NAME} (up)
--
-- Migrations run inside a transaction, so a failure here rolls back and leaves
-- the database dirty at version $((10#$NEXT - 1)). Prefer IF NOT EXISTS on
-- CREATE so a partially-applied deploy can be retried.

EOF

cat > "$DOWN" <<EOF
-- ${NEXT}_${NAME} (down)
--
-- Must undo the up migration exactly. A down that does not restore the previous
-- schema is worse than no down at all.

EOF

echo "  + $UP"
echo "  + $DOWN"
echo "  i the files are embedded automatically (go:embed migrations/*.sql)"
