#!/usr/bin/env bash
# authz-mutation-check.sh — prove the negative-authorization matrix would
# actually notice if the authorization boundary were broken.
#
# A green matrix proves the code passes the tests. It does not prove the tests
# would go red if the code were wrong, and for AUTHORIZATION tests the two are
# especially easy to confuse: a suite that asserts "403" against a middleware
# that denies everything is green, exhaustive-looking, and worth nothing.
#
# So this script breaks the boundary on purpose, one property at a time, and
# requires the matrix to go red each time. A mutation that SURVIVES is a hole in
# the tests and is reported as one.
#
# ─── The eight mutations ────────────────────────────────────────────────────
#
# They are the ones docs/security/AUTHORIZATION_MATRIX.md names, and each maps
# to a specific claim the slice makes:
#
#   1  skip the scope check on a write route     → the scope matrix
#   2  accept workspace A's key on workspace B   → tenant isolation
#   3  ignore credential revocation              → the kill switch
#   4  authorize from the workspace, not the key → cross-project isolation
#   5  refuse, but call the provider anyway      → state integrity on rejection
#   6  report a provider 403 as a caller 403     → error classification
#   7  let any scope read the audit trail        → audit isolation
#   8  fall back to another workspace's
#      connection when there is no active one    → the resolver
#
# ─── How it works, and why it is safe ───────────────────────────────────────
#
# The module is copied into a scratch directory and mutated THERE. The working
# tree is never written to, so an interrupted run cannot leave a sabotaged
# authorization boundary behind — which is the failure mode that makes
# "temporarily edit a file" tooling genuinely dangerous for this particular
# file set.
#
#   ./scripts/authz-mutation-check.sh
#   ./scripts/authz-mutation-check.sh -v     # show the failing test output
set -uo pipefail

VERBOSE=0
for arg in "$@"; do
  case "$arg" in
    -v|--verbose) VERBOSE=1 ;;
    *) echo "unknown flag: $arg"; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/lw-authz-mutation.XXXXXX")"
trap 'rm -rf "$WORKDIR"' EXIT

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mCAUGHT\033[0m   %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mSURVIVED\033[0m %s\n' "$1"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# A fresh copy of everything the affected packages compile AND read at runtime.
#
# The list is longer than the packages under test, and every entry earns its
# place: docs/ because internal/authz's OpenAPI gates read swagger.json and
# SDK_GO.md from it, sdk/ because the coverage gate reads apicoverage.json, and
# web/ because internal/server serves the console's assets from disk.
#
# A missing entry is worse than an inconvenience here. It makes unrelated tests
# fail in the scratch copy, every mutation is then "caught" by that failure, and
# the run reports eight green ticks while proving nothing — which is precisely
# the false confidence this script exists to prevent. verifyBaseline below is
# what makes that impossible rather than merely unlikely.
seed() {
  rm -rf "$WORKDIR/src"
  mkdir -p "$WORKDIR/src"
  for item in go.mod go.sum internal cmd sdk docs web config deploy; do
    [ -e "$REPO_ROOT/$item" ] && cp -R "$REPO_ROOT/$item" "$WORKDIR/src/"
  done
  rm -f "$WORKDIR/src/coverage.out" "$WORKDIR/src/sdk/go/coverage.out"
}

# verifyBaseline refuses to run a single mutation until the UNMUTATED scratch
# copy is green.
#
# Without it, a copy that is merely incomplete produces a perfect score. This is
# the one check in the script that is about the script.
verifyBaseline() {
  local packages="$1" output
  seed
  output="$(cd "$WORKDIR/src" && go test -count=1 $packages 2>&1)"
  if [ $? -ne 0 ]; then
    printf '\033[31m  the unmutated scratch copy is NOT green — every result below would be a lie\033[0m\n'
    printf '%s\n' "$output" | grep -E '^(---|\s+---) FAIL' | head -10 | sed 's/^/    /'
    exit 2
  fi
  printf '  baseline: the unmutated copy is green\n'
}

# mutate <description> <file> <python-snippet> <packages> [test-filter]
#
# The replacement is a small Python program run against the file's text, because
# sed's GNU/BSD differences are exactly the kind of thing that would silently
# apply no mutation and report a hole that is not there. A snippet that changes
# nothing is a hard failure, not a pass.
mutate() {
  local description="$1" file="$2" snippet="$3" packages="$4" filter="${5:-}"

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
    output="$(cd "$WORKDIR/src" && go test -count=1 -run "$filter" $packages 2>&1)"
  else
    output="$(cd "$WORKDIR/src" && go test -count=1 $packages 2>&1)"
  fi
  rc=$?

  if [ "$rc" -ne 0 ]; then
    # Name the tests that caught it. A mutation caught by one test is a
    # single point of failure in the evidence; a mutation caught by four is a
    # property that several independent assertions happen to depend on, which
    # is what a matrix is supposed to produce.
    local caught_by
    caught_by="$(printf '%s\n' "$output" | grep -E '^--- FAIL: ' | sed 's/^--- FAIL: //;s/ .*//' | sort -u)"
    ok "$description"
    if [ -n "$caught_by" ]; then
      printf '%s\n' "$caught_by" | head -8 | sed 's/^/             ↳ /'
    fi
    if [ "$VERBOSE" = "1" ]; then
      printf '%s\n' "$output" | grep -E '\.go:[0-9]+:' | head -4 | sed 's/^/               /'
    fi
  else
    bad "$description — the suite stayed green with the boundary broken"
    printf '%s\n' "$output" | tail -5 | sed 's/^/           /'
  fi
}

printf '\033[1mauthz-mutation-check — does the negative matrix actually bite?\033[0m\n'

AUTHZ=./internal/authz/...
SERVER=./internal/server/...
RUNTIME=./internal/identityruntime/...
PROJECT=./internal/project/...

verifyBaseline "$AUTHZ $SERVER $RUNTIME $PROJECT"

# ── 1. Skip the scope check on a write route ────────────────────────────────
#
# The single most likely regression: someone "fixes" a 403 a customer reported
# by exempting the route that produced it.
step "1 — a write route stops checking its scope"
mutate "POST /users no longer requires users:write" "internal/authz/registry.go" '
src = src.replace(
    """routeKey("POST", v1Workspace+"/users"):            scoped(ScopeUsersWrite),""",
    """routeKey("POST", v1Workspace+"/users"):            scoped(ScopeUsersRead),""")
' "$AUTHZ $SERVER"

# ── 2. Accept a credential outside its workspace ────────────────────────────
step "2 — the workspace binding stops being enforced"
mutate "a credential bound to A is accepted on B" "internal/authz/authorize.go" '
src = src.replace(
    "\tif !sameWorkspace(p.WorkspaceID, pathWorkspace) {",
    "\tif false && !sameWorkspace(p.WorkspaceID, pathWorkspace) {")
' "$AUTHZ $SERVER"

# ── 3. Ignore revocation ────────────────────────────────────────────────────
#
# Revocation is the kill switch the whole credential model rests on. A
# credential that keeps working after it is revoked makes every other guarantee
# here conditional on nobody having leaked a key.
step "3 — revocation stops taking effect"
mutate "a revoked credential still authenticates" "internal/project/model.go" '
src = src.replace(
    "func (c *Credential) IsUsable(now time.Time) bool {\n\treturn !c.IsRevoked() && !c.IsExpired(now)\n}",
    "func (c *Credential) IsUsable(now time.Time) bool {\n\treturn !c.IsExpired(now)\n}")
' "./internal/project/... $SERVER"

# ── 4. Authorize from the workspace rather than the credential ──────────────
#
# The cross-project failure mode, and the one that works perfectly in every
# single-project installation. Modelled here as scopes being unioned across the
# workspace: the credential presented no longer bounds what may be done.
step "4 — capabilities leak between projects in one workspace"
mutate "any scope in the workspace satisfies any route" "internal/authz/authorize.go" '
src = src.replace(
    "\tif !HasScope(p.Scopes, req.Scope) {",
    "\tif len(p.Scopes) == 0 && !HasScope(p.Scopes, req.Scope) {")
' "$AUTHZ $SERVER"

# ── 5. Refuse, and mutate anyway ────────────────────────────────────────────
#
# The catastrophic one: the caller is told 403 and the change happens. Every
# status-code assertion ever written passes. Modelled by letting the handler run
# before the refusal is written.
step "5 — a refused request still reaches the handler"
#
# The first attempt at this mutation appended c.Next() to deny(), and it
# SURVIVED — not because the matrix is weak, but because
# AbortWithStatusJSON has already moved gin's handler index past the end, so
# the extra Next() does nothing. A mutation that changes no behaviour proves
# nothing about the tests, and reporting it as caught would have been the exact
# false confidence this script exists to prevent.
#
# This form runs the chain BEFORE the refusal is written, which is what the
# failure actually looks like: the caller is told 403 and the mutation happened.
mutate "the handler runs, and the refusal is written afterwards" "internal/authz/authorize.go" '
src = src.replace(
    "func denyProject(c *gin.Context, p *auth.ProjectPrincipal, e *Error) {",
    "func denyProject(c *gin.Context, p *auth.ProjectPrincipal, e *Error) {\n\tc.Next()")
' "$AUTHZ $SERVER"

# ── 6. Confuse the two forbiddens ───────────────────────────────────────────
step "6 — a provider refusal is reported as a caller refusal"
mutate "provider_forbidden collapses into caller_forbidden" "internal/identityruntime/handler.go" '
src = src.replace(
    "\tcase errors.Is(err, identity.ErrProviderForbidden):\n\t\treturn ErrProviderForbidden",
    "\tcase errors.Is(err, identity.ErrProviderForbidden):\n\t\treturn ErrCallerForbidden")
' "$RUNTIME"

# ── 7. Let the audit trail be read by anything ──────────────────────────────
step "7 — the audit trail loses its own scope"
mutate "GET /audit accepts users:read" "internal/authz/registry.go" '
src = src.replace(
    """routeKey("GET", v1Workspace+"/audit"): scoped(ScopeAuditRead),""",
    """routeKey("GET", v1Workspace+"/audit"): scoped(ScopeUsersRead),""")
' "$AUTHZ $SERVER"

# ── 8. Fall back when there is no active connection ─────────────────────────
#
# The resolver's most dangerous possible kindness: a workspace with no
# connection quietly served by somebody else is a cross-tenant data leak that
# looks like a working feature.
step "8 — a workspace with no connection falls back elsewhere"
#
# The first attempt looked the connection up again under an empty workspace id,
# which resolves to nothing and fell through to the same error — no behaviour
# change, and it SURVIVED. This form serves the most recently built provider
# instead, which is a plausible "why rebuild it" optimisation and is a real
# cross-tenant leak: the workspace with no connection gets served by whichever
# tenant was busy last.
mutate "a missing connection is served from the provider cache" "internal/identityruntime/resolver.go" '
src = src.replace(
    "\tif conn == nil {\n\t\treturn nil, ErrConnectionMissing\n\t}",
    "\tif conn == nil {\n\t\tif p, ok := r.cache.any(); ok {\n\t\t\treturn &Resolved{WorkspacePublicID: ws.PublicID(), Provider: p,\n\t\t\t\tAccessMode: connection.AccessModeFull}, nil\n\t\t}\n\t\treturn nil, ErrConnectionMissing\n\t}")
src = src.replace(
    "func cacheKey(c *connection.Connection) string {",
    "func (c *providerCache) any() (identity.IdentityProvider, bool) {\n\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n\tif c.order == nil || c.order.Front() == nil {\n\t\treturn nil, false\n\t}\n\treturn c.order.Front().Value.(*entry).provider, true\n}\n\nfunc cacheKey(c *connection.Connection) string {")
' "$RUNTIME $SERVER"

# ── Summary ─────────────────────────────────────────────────────────────────
step "summary"
printf '  \033[32m%d caught\033[0m, \033[31m%d survived\033[0m\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo
  echo "  A surviving mutation means the matrix would not notice that break."
  echo "  Add the missing assertion; do not weaken the mutation."
  exit 1
fi
exit 0
