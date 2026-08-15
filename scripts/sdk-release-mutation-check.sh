#!/usr/bin/env bash
#
# sdk-release-mutation-check.sh — prove the release gates would actually notice a
# broken release.
#
# ─── Why release tooling especially needs this ──────────────────────────────
#
# A release gate is run rarely, by someone who wants it to pass, at the moment
# they are least inclined to question it. It has no natural failing cases in
# ordinary development, so a gate that silently checks nothing looks exactly like
# a gate that works — for months, until the release it was supposed to catch.
#
# So each mutation below breaks one release property on purpose and requires the
# corresponding gate to go red. A mutation that SURVIVES is a hole in the release
# tooling, reported as such.
#
# ─── No false-positive evidence ─────────────────────────────────────────────
#
# Every mutation is checked against a PRISTINE copy first. If the gate does not
# pass before the mutation, the run is aborted rather than reported: a gate that
# was already failing would "catch" every mutation while proving nothing. This is
# the discipline that separates mutation evidence from theatre.
#
# ─── Safety ────────────────────────────────────────────────────────────────
#
# The repository is never modified. A snapshot of the working tree is committed
# into a throwaway git repository under $TMPDIR — which the tag mutations need,
# since a tag is a git object and cannot be simulated with a file copy — and
# every mutation is applied there. `origin` is never contacted.
#
# Usage:  scripts/sdk-release-mutation-check.sh [-v]
# Exit:   0 = every mutation was caught · 1 = at least one survived

set -uo pipefail

VERBOSE=0
[ "${1:-}" = "-v" ] && VERBOSE=1

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$REPO_ROOT" || exit 1

WORK=$(mktemp -d "${TMPDIR:-/tmp}/lw-release-mutation.XXXXXX")
cleanup() { chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"; }
trap cleanup EXIT

PASS=0; FAIL=0
ok()   { PASS=$((PASS + 1)); printf '  \033[32mCAUGHT\033[0m    %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf '  \033[31mSURVIVED\033[0m  %s\n' "$1"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

VERSION=v0.1.0
TAG=sdk/go/$VERSION

# ─── The pristine release repository ────────────────────────────────────────
#
# Built once. Every mutation gets a fresh copy of it, so mutations cannot
# interact and a failure always names one cause.

step "Building a pristine release repository"

PRISTINE="$WORK/pristine"
mkdir -p "$PRISTINE"
git ls-files -co --exclude-standard -z | rsync -a --files-from=- --from0 "$REPO_ROOT/" "$PRISTINE/" ||
	{ echo "  ✗ snapshot failed"; exit 1; }

export GIT_CONFIG_GLOBAL="$WORK/gitconfig"
export GIT_CONFIG_SYSTEM=/dev/null
: > "$GIT_CONFIG_GLOBAL"
git config --global user.email mutation@localhost
git config --global user.name mutation
git config --global init.defaultBranch main

git -C "$PRISTINE" init -q
git -C "$PRISTINE" add -A
git -C "$PRISTINE" commit -q -m "release snapshot"
git -C "$PRISTINE" tag "$TAG"
echo "  + snapshot committed and tagged $TAG"

# The minimum-Go lane is skipped by default: it downloads and runs a second
# toolchain, and only one mutation is about it. That mutation turns it back on.
export SKIP_GO_MIN=1

# fresh — a clean copy of the pristine repository, echoed as its path.
#
# mktemp rather than an incrementing counter: `d=$(fresh)` runs the function in a
# SUBSHELL, so any variable it increments is lost and every mutation would
# silently share one directory — inheriting the previous mutation's damage and
# reporting it as a pristine failure.
fresh() {
	local d
	d=$(mktemp -d "$WORK/mut.XXXXXX")
	cp -R "$PRISTINE/." "$d/"
	printf '%s' "$d"
}

# edit <dir> <file> <python-old> <python-new>
#
# Fails loudly when the pattern matches nothing. A mutation that did not apply
# proves nothing, and counting it as caught is exactly the false confidence this
# script exists to prevent.
edit() {
	local dir="$1" file="$2" old="$3" new="$4"
	python3 - "$dir/$file" "$old" "$new" <<-'PY' || { echo "  ✗ mutation did not apply to $file"; exit 1; }
	import sys
	path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
	src = open(path).read()
	if old not in src:
	    sys.stderr.write("pattern not found in %s:\n%s\n" % (path, old))
	    sys.exit(1)
	open(path, "w").write(src.replace(old, new, 1))
	PY
}

# run <dir> <command...> — run a gate inside a copy; return its status.
#
# The output is kept in LAST_OUT rather than printed, so a caller can decide
# whether it is interesting. Re-running the command to see why it failed would
# not do: these gates write files, so the second run is not the first one.
LAST_OUT=""
run() {
	local dir="$1"; shift
	LAST_OUT=$( (cd "$dir" && "$@") 2>&1 )
	local rc=$?
	[ "$VERBOSE" = 1 ] && printf '%s\n' "$LAST_OUT" | tail -25 | sed 's/^/        /'
	return $rc
}

# expect_pristine <dir> <command...> — abort unless the gate passes unmutated.
expect_pristine() {
	local dir="$1"; shift
	if ! run "$dir" "$@"; then
		printf '  \033[31m✗ ABORT\033[0m the gate FAILS on a pristine copy, so no mutation below would prove anything.\n'
		printf '           command: %s\n' "$*"
		printf '%s\n' "$LAST_OUT" | tail -30 | sed 's/^/           /'
		exit 1
	fi
}

# ─── Mutations ──────────────────────────────────────────────────────────────

step "1. A root-style tag is accepted for the nested SDK module"
# The single most consequential release mistake: `v0.1.0` publishes the ROOT
# module, and the SDK stays unresolvable while looking released.
D=$(fresh)
git -C "$D" tag "$VERSION"
expect_pristine "$D" ./scripts/check-sdk-release.sh --tag "$TAG"
if run "$D" ./scripts/check-sdk-release.sh --tag "$VERSION"; then
	bad "a root 'v0.1.0' tag was accepted as an SDK release"
else
	ok "a root 'v0.1.0' tag is rejected — only the sdk/go/ prefix publishes the SDK"
fi

step "2. The release gate stops running the SDK's tests"
# Removing a check and breaking the thing it checked must not both pass.
D=$(fresh)
expect_pristine "$D" ./scripts/check-sdk-release.sh --tag "$TAG"
edit "$D" sdk/go/client.go \
	'func (c *Client) WorkspaceID() string { return c.workspace }' \
	'func (c *Client) WorkspaceID() string { return "" }'
if run "$D" ./scripts/check-sdk-release.sh --tag "$TAG"; then
	bad "a broken SDK was released — the gate does not run the tests"
else
	ok "broken SDK behaviour fails the release gate"
	# ...and now remove the tests from the gate, which must ALSO be caught,
	# because otherwise the previous line only proves the tests exist today.
	edit "$D" scripts/check-sdk-release.sh \
		'run_gate "go test"                   env -C "$SDK_MODULE_DIR" "$GO" test -count=1 ./...' \
		'# removed'
	if run "$D" ./scripts/check-sdk-release.sh --tag "$TAG"; then
		bad "deleting the test step from the gate let a broken SDK through"
	else
		ok "even with the test step deleted, other gates still refuse the release"
	fi
fi

step "3. The module path changes without the release prefix changing"
D=$(fresh)
expect_pristine "$D" ./scripts/check-sdk-release.sh --worktree "$VERSION"
edit "$D" sdk/go/go.mod \
	'module github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go' \
	'module github.com/JoaoGabrielVianna/lightweight-saas-backend/client/go'
if run "$D" ./scripts/check-sdk-release.sh --worktree "$VERSION"; then
	bad "the module path and the tag prefix were allowed to disagree"
else
	ok "a module path that does not match its directory fails the release identity check"
fi

step "4. A third-party runtime dependency enters the SDK"
D=$(fresh)
expect_pristine "$D" make -s sdk-deps-check
edit "$D" sdk/go/go.mod \
	'go 1.23.0' \
	'go 1.23.0

require golang.org/x/time v0.12.0'
if run "$D" make -s sdk-deps-check; then
	bad "the SDK acquired a dependency and the zero-dependency gate passed"
else
	ok "a third-party dependency fails the zero-dependency gate"
fi

step "5. An exported SDK method is removed"
D=$(fresh)
expect_pristine "$D" make -s sdk-api-check
edit "$D" sdk/go/client.go \
	'func (c *Client) BaseURL() string { return c.baseURL.String() }' \
	''
if run "$D" make -s sdk-api-check; then
	bad "an exported method disappeared with no API drift reported"
else
	ok "removing an exported method fails the public-API snapshot gate"
fi

step "5b. An exported error code's VALUE changes while its name stays"
# The subtler half: a consumer compares against the constant, but what crosses
# the wire is the string, so the identifier alone is not the promise.
D=$(fresh)
if grep -q 'CodeInsufficientScope' "$D/sdk/go/errors.go"; then
	edit "$D" sdk/go/errors.go 'CodeInsufficientScope = "insufficient_scope"' \
		'CodeInsufficientScope = "scope_insufficient"'
	if run "$D" make -s sdk-api-check; then
		bad "an error code's wire value changed with no API drift reported"
	else
		ok "changing a wire-visible constant's value fails the API snapshot gate"
	fi
fi

step "6. The docs publish a go get command with the wrong module path"
D=$(fresh)
expect_pristine "$D" ./scripts/check-sdk-release.sh --worktree "$VERSION"
edit "$D" sdk/go/README.md \
	'go get github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go@v0.1.0' \
	'go get github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk@v0.1.0'
if run "$D" ./scripts/check-sdk-release.sh --worktree "$VERSION"; then
	bad "the README documented a module path that does not exist"
else
	ok "a wrong module path in the install command fails the release gate"
fi

step "6b. The docs tell consumers to ask for the TAG instead of the version"
# The regression that was actually present in this repository before Slice 16.
D=$(fresh)
edit "$D" sdk/go/README.md \
	'go get github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go@v0.1.0' \
	'go get github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go@sdk/go/v0.1.0'
if run "$D" ./scripts/check-sdk-release.sh --worktree "$VERSION"; then
	bad "the README told consumers to query the tag rather than the version"
else
	ok "documenting the tag as the version query fails the release gate"
fi

step "7. The SDK reports a fake version in its User-Agent"
D=$(fresh)
expect_pristine "$D" make -s sdk-test
edit "$D" sdk/go/version.go \
	'func sdkVersion() string {' \
	'func sdkVersion() string {
	return "v1.0.0" // invented'
if run "$D" make -s sdk-test; then
	bad "a hard-coded version was reported and the suite stayed green"
else
	ok "an invented version fails the SDK suite (Version() must be \"dev\" in a test binary)"
fi

step "8. A tag is validated even though its commit has no sdk/go/go.mod"
# The exact hazard this repository is in today: the SDK is untracked, so a tag on
# the current HEAD would publish nothing at all.
D=$(fresh)
git -C "$D" rm -r -q --cached sdk/go
git -C "$D" commit -q -m "remove the SDK from tracked content"
git -C "$D" tag "sdk/go/v0.2.0"
if run "$D" ./scripts/check-sdk-release.sh --tag "sdk/go/v0.2.0"; then
	bad "a tag whose commit contains no SDK module was accepted"
else
	ok "a tag whose commit has no sdk/go/go.mod is refused"
fi

step "8b. A tag is validated while the working tree is a different commit"
# The content checks read the tag's git objects; the tests necessarily run
# against the checked-out tree. If those disagree, the report mixes two releases.
D=$(fresh)
expect_pristine "$D" ./scripts/check-sdk-release.sh --tag "$TAG"
echo "a later change" >> "$D/sdk/go/README.md"
git -C "$D" add -A
git -C "$D" commit -q -m "a commit after the tag"
if run "$D" ./scripts/check-sdk-release.sh --tag "$TAG"; then
	bad "a tag was validated from a working tree at a different commit"
else
	ok "validating a tag requires the working tree to be at that tag"
fi

step "9. An invalid SemVer version is accepted"
D=$(fresh)
for v in "sdk/go/0.1.0" "sdk/go/v0.1" "sdk/go/v0.01.0" "sdk/go/vlatest"; do
	git -C "$D" tag "$v" 2>/dev/null
	if run "$D" ./scripts/check-sdk-release.sh --tag "$v"; then
		bad "'$v' was accepted as a release tag"
	else
		ok "'$v' is rejected"
	fi
done

step "10. A v2 release is tagged without the /v2 module path suffix"
# Go does not error on this. It simply never resolves, for everyone, forever.
D=$(fresh)
git -C "$D" tag "sdk/go/v2.0.0"
if run "$D" ./scripts/check-sdk-release.sh --tag "sdk/go/v2.0.0"; then
	bad "v2.0.0 was accepted on a module path with no /v2 suffix"
else
	ok "v2.0.0 without a /v2 module path suffix is refused"
fi

step "11. The SDK uses a symbol newer than the Go version it promises"
# The go directive is a promise to consumers on older toolchains, and the
# machine's own newer Go cannot detect it being broken.
D=$(fresh)
unset SKIP_GO_MIN
edit "$D" sdk/go/version.go \
	'import (' \
	'import (
	"testing/synctest" // added in Go 1.24, after the 1.23 this module promises'
if run "$D" ./scripts/check-sdk-release.sh --worktree "$VERSION"; then
	bad "code needing a newer Go than the module promises was accepted"
else
	ok "a symbol newer than the declared minimum Go fails the release gate"
fi
export SKIP_GO_MIN=1

step "12. A dirty module definition ships"
D=$(fresh)
edit "$D" sdk/go/go.mod \
	'go 1.23.0' \
	'go 1.23.0

require example.com/phantom v1.0.0 // never imported: go mod tidy would remove it'
if run "$D" ./scripts/check-sdk-release.sh --worktree "$VERSION"; then
	bad "an untidy go.mod was accepted for release"
else
	ok "a go.mod that go mod tidy would change fails the release gate"
fi

# ─── Verdict ────────────────────────────────────────────────────────────────

printf '\n\033[1m== Result\033[0m\n'
printf '  %d caught · %d survived\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then
	printf '  \033[32m+ every release mutation was caught\033[0m\n'
	exit 0
fi
printf '  \033[31m✗ %d mutation(s) survived — the release tooling has that many blind spots\033[0m\n' "$FAIL"
exit 1
