#!/usr/bin/env bash
# audit-mutation-check.sh — prove the transactional-audit suite would actually
# notice if atomicity were broken.
#
# A green acceptance matrix proves the code passes the tests. It does not prove
# the tests would go red if the code were wrong, and for TRANSACTION tests the
# two are unusually easy to confuse: a suite that asserts "the row is absent"
# against an implementation that never wrote it is green, thorough-looking, and
# worth nothing.
#
# So this script breaks the guarantee on purpose, one property at a time, and
# requires the suite to go red each time. A mutation that SURVIVES is a hole in
# the tests and is reported as one.
#
# ─── The eight mutations ────────────────────────────────────────────────────
#
#   1  move the audit write AFTER the commit    → the window TD-033 described
#   2  ignore the audit insert error            → mutation commits regardless
#   3  commit despite the callback failing      → the runner betrays the service
#   4  write audit through the pool, not the tx → two transactions again
#   5  drop request-id attribution              → the row cannot be correlated
#   6  attribute every event to one workspace   → one tenant's history in
#                                                 another tenant's trail
#   7  widen the metadata allowlist and leak    → a credential in the trail
#   8  remove a required event's classification → the drift gate
#
# ─── Why this needs a database ──────────────────────────────────────────────
#
# Mutations 1–6 are about what PostgreSQL does on ROLLBACK, and only PostgreSQL
# can answer that. The suite that catches them is build-tagged `integration`, so
# this script requires DB_URL and runs with that tag. Running it without one
# would report eight passes against a suite that skipped.
#
# ─── How it works, and why it is safe ───────────────────────────────────────
#
# The module is copied into a scratch directory and mutated THERE. The working
# tree is never written to, so an interrupted run cannot leave a sabotaged
# audit path behind.
#
#   DB_URL=postgres://…  ./scripts/audit-mutation-check.sh
#   DB_URL=postgres://…  ./scripts/audit-mutation-check.sh -v
set -uo pipefail

VERBOSE=0
for arg in "$@"; do
  case "$arg" in
    -v|--verbose) VERBOSE=1 ;;
    *) echo "unknown flag: $arg"; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/lw-audit-mutation.XXXXXX")"
trap 'rm -rf "$WORKDIR"' EXIT

if [ -z "${DB_URL:-}" ]; then
  echo "  DB_URL is required, and must point at a THROWAWAY database." >&2
  echo "  Mutations 1-6 are about what PostgreSQL does on ROLLBACK; without a" >&2
  echo "  database the suite that catches them SKIPS, and every mutation would" >&2
  echo "  be reported as caught while nothing was tested." >&2
  exit 2
fi

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mCAUGHT\033[0m   %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mSURVIVED\033[0m %s\n' "$1"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# The packages the guarantee lives in, plus everything their tests read.
seed() {
  rm -rf "$WORKDIR/src"
  mkdir -p "$WORKDIR/src"
  for item in go.mod go.sum internal cmd sdk docs web config deploy; do
    [ -e "$REPO_ROOT/$item" ] && cp -R "$REPO_ROOT/$item" "$WORKDIR/src/"
  done
  rm -f "$WORKDIR/src/coverage.out" "$WORKDIR/src/sdk/go/coverage.out"
}

PACKAGES="./internal/auditlog/... ./internal/workspace/... ./internal/project/... ./internal/connection/... ./internal/logging/..."

# verifyBaseline refuses to run a single mutation until the UNMUTATED scratch
# copy is green.
#
# Slice 14 learned this the expensive way: a scratch copy missing one directory
# made unrelated tests fail, every mutation was "caught" by that failure, and
# the run reported a perfect score while proving nothing. This is the one check
# in the script that is about the script.
verifyBaseline() {
  seed
  local output
  output="$(cd "$WORKDIR/src" && DB_URL="$DB_URL" go test -tags=integration -count=1 $PACKAGES 2>&1)"
  if [ $? -ne 0 ]; then
    printf '\033[31m  the unmutated scratch copy is NOT green — every result below would be a lie\033[0m\n'
    printf '%s\n' "$output" | grep -E '^(---|\s+---) FAIL|^FAIL' | head -12 | sed 's/^/    /'
    exit 2
  fi
  # And the atomicity suite must have RUN, not skipped.
  if printf '%s\n' "$output" | grep -q 'no test files'; then :; fi
  local ran
  ran="$(cd "$WORKDIR/src" && DB_URL="$DB_URL" go test -tags=integration -count=1 -run TestAtomicity -v \
        ./internal/auditlog/ 2>&1 | grep -c '^--- PASS')"
  if [ "${ran:-0}" -lt 10 ]; then
    printf '\033[31m  only %s atomicity tests ran — the suite is skipping, so nothing below is tested\033[0m\n' "$ran"
    exit 2
  fi
  printf '  baseline: the unmutated copy is green, %s atomicity tests ran\n' "$ran"
}

# mutate <description> <file> <python-snippet> [test-filter]
mutate() {
  local description="$1" file="$2" snippet="$3" filter="${4:-}"

  seed

  if ! python3 - "$WORKDIR/src/$file" <<PY
import sys
path = sys.argv[1]
src = open(path).read()
before = src
$snippet
if src == before:
    sys.stderr.write("the mutation matched nothing\n")
    sys.exit(3)
open(path, "w").write(src)
PY
  then
    bad "$description — the mutation did not apply, so nothing was tested"
    return
  fi

  local output rc
  if [ -n "$filter" ]; then
    output="$(cd "$WORKDIR/src" && DB_URL="$DB_URL" go test -tags=integration -count=1 -run "$filter" $PACKAGES 2>&1)"
  else
    output="$(cd "$WORKDIR/src" && DB_URL="$DB_URL" go test -tags=integration -count=1 $PACKAGES 2>&1)"
  fi
  rc=$?

  if [ "$rc" -ne 0 ]; then
    local caught_by
    caught_by="$(printf '%s\n' "$output" | grep -E '^--- FAIL: ' | sed 's/^--- FAIL: //;s/ .*//' | sort -u)"
    ok "$description"
    if [ -n "$caught_by" ]; then
      printf '%s\n' "$caught_by" | head -6 | sed 's/^/             ↳ /'
    fi
    if [ "$VERBOSE" = "1" ]; then
      printf '%s\n' "$output" | grep -E '_test\.go:[0-9]+:' | head -4 | sed 's/^/               /'
    fi
  else
    bad "$description — the suite stayed green with the guarantee broken"
    printf '%s\n' "$output" | tail -4 | sed 's/^/           /'
  fi
}

# mutate_expect_green applies edits and requires the suite to stay GREEN.
#
# It exists for one case, and the case earns the extra function: proving a
# defence works means showing the attack succeeds only when the defence is ALSO
# removed. A mutation that "survives" is normally a hole; here it is the
# evidence, and reporting it as a failure would have been backwards.
mutate_expect_green() {
  local description="$1"
  shift
  if ! apply_edits "$@"; then
    bad "$description — the mutation did not apply, so nothing was tested"
    return
  fi

  local output
  output="$(cd "$WORKDIR/src" && DB_URL="$DB_URL" go test -tags=integration -count=1 $PACKAGES 2>&1)"
  if [ $? -eq 0 ]; then
    ok "$description"
  else
    bad "$description — the suite went red, so this is not the defence it claims to be"
    printf '%s\n' "$output" | grep -E '^--- FAIL' | head -3 | sed 's/^/           /'
  fi
}

# mutate_multi applies several file/snippet pairs to ONE scratch copy and
# requires the suite to go red.
mutate_multi() {
  local description="$1"
  shift
  if ! apply_edits "$@"; then
    bad "$description — a mutation did not apply, so nothing was tested"
    return
  fi

  local output rc
  output="$(cd "$WORKDIR/src" && DB_URL="$DB_URL" go test -tags=integration -count=1 $PACKAGES 2>&1)"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    local caught_by
    caught_by="$(printf '%s\n' "$output" | grep -E '^--- FAIL: ' | sed 's/^--- FAIL: //;s/ .*//' | sort -u)"
    ok "$description"
    [ -n "$caught_by" ] && printf '%s\n' "$caught_by" | head -6 | sed 's/^/             ↳ /'
  else
    bad "$description — the suite stayed green with the guarantee broken"
  fi
}

# apply_edits seeds a fresh copy and applies file/snippet pairs.
# Each snippet is the PATH of a python fragment, not the fragment itself, so no
# heredoc can collide with the script that carries it.
apply_edits() {
  seed
  while [ $# -gt 0 ]; do
    local file="$1" fragment="$2"
    shift 2
    if ! python3 "$REPO_ROOT/scripts/lib/apply-mutation.py" "$WORKDIR/src/$file" "$fragment"; then
      return 1
    fi
  done
  return 0
}

printf '\033[1maudit-mutation-check — does the atomicity suite actually bite?\033[0m\n'
verifyBaseline

# ── 1. The audit write moves after the commit ───────────────────────────────
#
# This is TD-033 itself, reintroduced: the mutation commits, then the audit row
# is attempted separately. It is the single most likely regression, because it
# is what the code looked like before this slice and it reads perfectly fine.
step "1 — the audit write happens after the transaction commits"
mutate "workspace.create audits after COMMIT" "internal/workspace/service.go" '
src = src.replace(
    """	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		if err := s.repo.WithTx(tx).Create(ctx, w); err != nil {
			return err
		}
		ev.Workspace = w.PublicID()
		ev.Target = audit.Target{Kind: targetKind, ID: w.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return w, nil""",
    """	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		return s.repo.WithTx(tx).Create(ctx, w)
	}); err != nil {
		return nil, err
	}
	ev.Workspace = w.PublicID()
	ev.Target = audit.Target{Kind: targetKind, ID: w.PublicID()}
	_ = s.audit.RecordTx(ctx, nil, *ev)
	return w, nil""")
'

# ── 2. The audit error is ignored ───────────────────────────────────────────
step "2 — the audit insert error is swallowed"
mutate "workspace.create ignores the audit failure" "internal/workspace/service.go" '
src = src.replace(
    """		ev.Target = audit.Target{Kind: targetKind, ID: w.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)""",
    """		ev.Target = audit.Target{Kind: targetKind, ID: w.PublicID()}
		_ = s.audit.RecordTx(ctx, tx, *ev)
		return nil""")
'

# ── 3. The runner commits despite the callback failing ──────────────────────
#
# The service is correct and the seam betrays it. Worth its own mutation
# because it is the one place a single edit silently disables the guarantee for
# EVERY operation at once.
step "3 — the transaction runner commits even when the callback fails"
mutate "InTx swallows the callback error" "internal/database/tx.go" '
src = src.replace(
    """	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})""",
    """	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_ = fn(tx)
		return nil
	})""")
'

# ── 4. The audit row is written through the pool ────────────────────────────
#
# The subtlest of the eight: everything still looks transactional, the service
# still checks the error, and the row goes to a DIFFERENT connection — so a
# rollback leaves the audit row behind. This is what TD-033 would have become if
# someone had "added a transaction" without binding the audit store to it.
step "4 — the audit row is written through the pool instead of the transaction"
mutate "the audit store ignores the transaction" "internal/auditlog/repository.go" '
src = src.replace(
    """func (r *Repository) WithTx(tx database.Tx) Store {
	if tx == nil {
		return r
	}
	return &Repository{db: tx}
}""",
    """func (r *Repository) WithTx(tx database.Tx) Store {
	return r
}""")
'

# ── 5. Request-id attribution is dropped ────────────────────────────────────
step "5 — the durable row loses its request id"
mutate "toRecord drops the request id" "internal/auditlog/recorder.go" '
src = src.replace(
    """		RequestID:  e.RequestID,""",
    """		RequestID:  "",""")
'

# ── 6. Events are attributed to the wrong workspace ─────────────────────────
#
# The FK makes an INVENTED workspace fail loudly, which would be caught for the
# wrong reason. This models the defect that actually hides: every event
# attributed to the first workspace the process ever saw — a real id, a valid
# FK, and one tenant reading another tenant'"'"'s history.
step "6 — every event is attributed to the same workspace"
mutate "workspace attribution is pinned to the first one seen" "internal/auditlog/recorder.go" '
src = src.replace(
    """		rec.WorkspaceID = uuid""",
    """		if stickyWorkspace == "" {
			stickyWorkspace = uuid
		}
		rec.WorkspaceID = stickyWorkspace""")
src = src.replace(
    """var log = logger.New(\"auditlog\")""",
    """var log = logger.New(\"auditlog\")

var stickyWorkspace string""")
'

# ── 7. A credential leaks into the audit event ──────────────────────────────
#
# Putting the minted token into the event's Extra. This is the realistic shape
# of the mistake: Extra is the one free-text region of an event, the scope list
# already lives there, and adding "one more useful field" is a small edit.
step "7 — a credential secret is put into the audit event"
mutate_multi "the token is added to the event's metadata" \
  "internal/project/service.go" "$REPO_ROOT/scripts/lib/audit-mutations/leak-token-into-event.py"

# ── 7b. …and the allowlist widened so it would reach the ROW ────────────────
#
# The same edit plus the second one needed for the secret to reach the TABLE,
# because allowlistMetadata drops keys the event type does not declare.
#
# Both are run because they fail at different layers, and knowing which is which
# is the useful part: 7 is caught while the secret is still in memory
# (TestHTTP_AuditAttribution asserts no event ever carries it) and 7b is caught
# again at the row (TestIntegration_NoSecretReachesTheTable). Two independent
# barriers, and the run shows both holding rather than one hiding the other.
step "7b — …with the allowlist widened so it would reach the row"
mutate_multi "the token is added to the event AND admitted by the allowlist" \
  "internal/project/service.go"    "$REPO_ROOT/scripts/lib/audit-mutations/leak-token-into-event.py" \
  "internal/auditlog/redaction.go" "$REPO_ROOT/scripts/lib/audit-mutations/widen-metadata-allowlist.py"

# ── 8. A required event loses its classification ────────────────────────────
step "8 — a control-plane mutation loses its audit classification"
mutate "credential.revoked is removed from the coverage registry" "internal/auditlog/coverage.go" '
src = src.replace(
    """	routeKey(\"POST\", v1Project+\"/credentials/:credential_id/revoke\"): atomic(audit.ActionCredentialRevoked),""",
    """""")
'

# ── Summary ─────────────────────────────────────────────────────────────────
step "summary"
printf '  \033[32m%d caught\033[0m, \033[31m%d survived\033[0m\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo
  echo "  A surviving mutation means the suite would not notice that break."
  echo "  Add the missing assertion; do not weaken the mutation."
  exit 1
fi
exit 0
