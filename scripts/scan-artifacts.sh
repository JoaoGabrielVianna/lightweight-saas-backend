#!/usr/bin/env bash
# scan-artifacts.sh — prove that a browser e2e run published no secret.
#
# ─── Why this is a gate and not a comment in a config file ──────────────────
#
# The browser suite handles three things that must never outlive the run: an
# identity-provider client secret the operator types into a form, a project
# credential the server shows exactly once, and the operator's bearer token.
# Playwright is very good at capturing all three — a trace holds DOM snapshots
# including input values, a failure screenshot holds whatever was on screen,
# and an HTML report embeds both.
#
# playwright.config.js turns trace, screenshot and video off, which is the
# smallest safe policy. This script is what makes that claim checkable: it
# searches everything the run produced for the values the run actually used. A
# policy nobody verifies is a policy that survives exactly until someone
# enables tracing to debug a flake.
#
# ─── What it searches for ───────────────────────────────────────────────────
#
#   1. Values the tests recorded in the SENTINEL directory — the connection
#      secret typed into the form, and the one-time project credential the
#      server returned. Exact strings, so a hit is unambiguous.
#   2. Shapes: lw_sk_ credentials, JWTs, Authorization: Bearer headers.
#      Catches a secret that took a path nobody registered.
#
# The sentinel directory is NEVER searched and is never published: it is the
# list of things to look for, not a thing to look at. The caller creates it
# outside the artifact tree.
#
# Usage:
#   scripts/scan-artifacts.sh <artifact-dir> <sentinel-dir> [extra-file ...]
set -uo pipefail

if [ $# -lt 2 ]; then
  echo "usage: $0 <artifact-dir> <sentinel-dir> [extra-file ...]" >&2
  exit 2
fi

ARTIFACT_DIR="$1"; shift
SENTINEL_DIR="$1"; shift
EXTRA=("$@")

if [ ! -d "$ARTIFACT_DIR" ]; then
  echo "  scan: no artifact directory at $ARTIFACT_DIR — nothing produced, nothing to leak"
  exit 0
fi

# Collect the targets: everything under the artifact dir, plus any extra files
# named explicitly (the API log lives outside the artifact tree but is copied
# into diagnostics, so it is in scope).
targets=()
while IFS= read -r f; do targets+=("$f"); done < <(find "$ARTIFACT_DIR" -type f 2>/dev/null)
for f in ${EXTRA[@]+"${EXTRA[@]}"}; do
  [ -f "$f" ] && targets+=("$f")
done

if [ ${#targets[@]} -eq 0 ]; then
  echo "  scan: 0 files produced — nothing to leak"
  exit 0
fi

# The sentinel file is one secret per line, written by the tests as they see
# each value. Absent means the suite never got far enough to see one, which is
# a fact worth printing rather than a silent pass.
SENTINEL_FILE="$SENTINEL_DIR/secrets.txt"
sentinel_count=0
if [ -f "$SENTINEL_FILE" ]; then
  sentinel_count="$(grep -c . "$SENTINEL_FILE" 2>/dev/null || echo 0)"
fi

echo "  scan: ${#targets[@]} artifact file(s), ${sentinel_count} recorded secret(s)"

if [ "$sentinel_count" = "0" ]; then
  echo "  scan: ⚠ no secrets were recorded by the suite."
  echo "        Either no journey reached a secret, or the recording helper is not wired."
fi

FAILED=0

# ── 1. Exact recorded values ────────────────────────────────────────────────
if [ -f "$SENTINEL_FILE" ] && [ "$sentinel_count" != "0" ]; then
  while IFS= read -r secret; do
    [ -z "$secret" ] && continue
    # -F: the recorded value is a literal, never a pattern. A credential
    # containing a regex metacharacter would otherwise silently match nothing.
    if hits="$(grep -rlF -- "$secret" ${targets[@]+"${targets[@]}"} 2>/dev/null)"; then
      if [ -n "$hits" ]; then
        echo "  scan: ✗ a recorded secret appears in published artifacts:" >&2
        # The value itself is NOT printed. Printing it here would put the
        # secret in the CI log, which is the thing being prevented.
        printf '        %s\n' "${secret:0:12}…" >&2
        printf '        in: %s\n' $hits >&2
        FAILED=1
      fi
    fi
  done < "$SENTINEL_FILE"
fi

# ── 2. Shapes ───────────────────────────────────────────────────────────────
#
# Deliberately the same vocabulary as scripts/redact-logs.sh. That script
# REMOVES these from logs before upload; this one asserts none survived. Two
# halves of one rule, and they must not drift.
shape_scan() { # label pattern
  local label="$1" pattern="$2" hits
  hits="$(grep -rlE -- "$pattern" ${targets[@]+"${targets[@]}"} 2>/dev/null)"
  if [ -n "$hits" ]; then
    echo "  scan: ✗ $label found in published artifacts:" >&2
    printf '        %s\n' $hits >&2
    FAILED=1
  fi
}

shape_scan "a project credential (lw_sk_…)" 'lw_sk_[A-Za-z0-9_]{8,}'
shape_scan "a JWT" 'eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}'
shape_scan "an Authorization: Bearer header" '[Aa]uthorization["'"'"']?\s*[:=]\s*["'"'"']?[Bb]earer +[A-Za-z0-9._-]{8,}'

# Master-key material. Added with rotation: an operator now handles several
# keys at once, editing a keyring by hand, so the chance of one landing in a
# log, a shell transcript or an environment dump went up rather than down.
#
# The patterns look for the SHAPE of a configured key rather than for a
# particular value, because the value is what must never be written down for
# the scanner to look for. A 32-byte key is 43 base64 characters plus optional
# padding; the version prefix is what makes a keyring entry unmistakable.
#
# Note both require a NON-EMPTY value: `SECRETS_MASTER_KEY=` appears in
# .env.example and in every artifact that echoes a template, and flagging that
# would train everyone to ignore this check.
shape_scan "a SECRETS_KEYRING entry (version:key)" \
  '[0-9]{1,4}:[A-Za-z0-9+/]{42,}={0,2}'
shape_scan "an assigned master key" \
  'SECRETS_(MASTER_KEY|KEYRING)["'"'"']?\s*[:=]\s*["'"'"']?[A-Za-z0-9+/:,]{20,}'

if [ "$FAILED" != "0" ]; then
  echo >&2
  echo "  The artifact policy is: no trace, no screenshot, no video (see" >&2
  echo "  tests/browser/playwright.config.js). A hit above means either the" >&2
  echo "  policy was relaxed, or a test wrote a secret somewhere itself." >&2
  echo "  Do NOT fix this by redacting after the fact unless you also prove" >&2
  echo "  the published copy is the redacted one." >&2
  exit 1
fi

echo "  scan: ✓ zero matches — no credential, token or bearer header in any artifact"
exit 0
