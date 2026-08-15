#!/usr/bin/env bash
#
# check-sdk-release.sh — answer one question: if this were released as SDK
# version X, would the release be valid?
#
# ─── What this script will never do ─────────────────────────────────────────
#
# It does not `git tag`, `git push`, `gh release`, or write to any remote. It is
# a script a maintainer runs while nervous, so it is built to be safe to run
# while nervous: every operation is a read. Publishing stays a deliberate,
# separately-typed act, because a release script one typo away from publishing
# is a release script that eventually publishes a typo.
#
# ─── The three modes, and why there are three ───────────────────────────────
#
#   --worktree vX.Y.Z   Development. Validates the FILES ON DISK. Release
#                       preconditions (clean tree, committed SDK, reviewed on
#                       main) are reported but do not fail the run.
#
#   --head vX.Y.Z       Release preflight. Validates the COMMIT AT HEAD, which
#                       is what a tag would actually capture, and enforces every
#                       precondition.
#
#   --tag sdk/go/vX.Y.Z Validates an EXISTING tag. What CI runs.
#
#   --identity          The cheap subset: module-path/tag-prefix coherence and
#                       the install command in the docs. No tests, no toolchain
#                       download, no git reads. Runs on every push.
#
# The split exists because git tags point at commits, and the working tree is not
# the commit. A check that validated the files on disk and then reported "ready
# to tag" would be telling a precise lie whenever the two differ: a tag captures
# the commit and nothing that is only on disk. `--worktree` says what it checked;
# `--head` says what a tag would contain. Those are different sentences and this
# script refuses to blur them.
#
# Usage:
#   scripts/check-sdk-release.sh --worktree v0.1.0
#   scripts/check-sdk-release.sh --head v0.1.0
#   scripts/check-sdk-release.sh --tag sdk/go/v0.1.0
#   scripts/check-sdk-release.sh --identity
#
# Exit: 0 = would be a valid release · 1 = would not

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export REPO_ROOT
cd "$REPO_ROOT" || exit 1

# shellcheck source=scripts/lib/sdk-release.sh
. "$REPO_ROOT/scripts/lib/sdk-release.sh"

GO="${GO:-go}"

FAILURES=0
WARNINGS=0

pass() { printf '  \033[32m+\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mx\033[0m %s\n' "$*"; FAILURES=$((FAILURES + 1)); }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; WARNINGS=$((WARNINGS + 1)); }
note() { printf '      %s\n' "$*"; }
head2() { printf '\n\033[1m%s\033[0m\n' "$*"; }

usage() {
	sed -n '3,36p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
	exit 2
}

# ─── Arguments ──────────────────────────────────────────────────────────────

MODE=""
VERSION=""
TAG=""
SOURCE_DESC="the module's declared identity and the installation docs"

case "${1:-}" in
	--worktree) MODE=worktree; VERSION="${2:-}" ;;
	--head)     MODE=head;     VERSION="${2:-}" ;;
	--tag)      MODE=tag;      TAG="${2:-}" ;;
	# --identity is the cheap subset: module-path/tag-prefix coherence and the
	# install command in the docs, with no tests, no toolchain download and no
	# git object reads. It runs on EVERY push as part of `make sdk-check`,
	# because those two facts drift by ordinary editing — someone renames the
	# module, someone rewrites a README — and catching that only at tag time
	# would be catching it after the wrong command is already published.
	--identity) MODE=identity; VERSION="${2:-v0.0.0}" ;;
	*)          usage ;;
esac

# ─── 1. Release identity ────────────────────────────────────────────────────
#
# Everything downstream is expressed in terms of the values derived here, so if
# this fails nothing else is worth reporting.

head2 "Release identity"

if ! sdk_release_identity; then
	fail "the SDK's module path and its location disagree — see above"
	exit 1
fi
pass "root module      $ROOT_MODULE_PATH"
pass "SDK module       $SDK_MODULE_PATH"
pass "SDK directory    $SDK_MODULE_DIR"
pass "tag prefix       $SDK_TAG_PREFIX/   (derived from the two module paths)"
pass "SDK minimum Go   $SDK_GO_DIRECTIVE"

# In --tag mode the version is recovered from the tag, which is also the first
# thing that can be wrong about a tag.
if [ "$MODE" = tag ]; then
	[ -n "$TAG" ] || usage
	if ! VERSION=$(sdk_version_of_tag "$TAG"); then
		fail "tag '$TAG' is not an SDK release tag"
		note "An SDK release tag must begin with '$SDK_TAG_PREFIX/', because that is the"
		note "directory the module lives in and Go looks for no other prefix."
		note ""
		note "  wrong   v0.1.0          publishes the ROOT module, not the SDK"
		note "  wrong   sdk/v0.1.0      no module lives at sdk/"
		note "  wrong   go/v0.1.0       no module lives at go/"
		note "  right   $SDK_TAG_PREFIX/v0.1.0"
		exit 1
	fi
	pass "tag '$TAG' publishes version $VERSION"
else
	[ -n "$VERSION" ] || usage
	TAG=$(sdk_tag_for "$VERSION")
	pass "version $VERSION would be published by tag '$TAG'"
fi

# ─── 2. Version syntax and major-version rule ───────────────────────────────

head2 "Version $VERSION"

if is_valid_semver "$VERSION"; then
	pass "valid SemVer"
else
	fail "'$VERSION' is not valid SemVer"
	note "Go requires vMAJOR.MINOR.PATCH with a leading 'v' and no leading zeros."
	note "  wrong   0.1.0     no leading v"
	note "  wrong   v0.1      no patch component"
	note "  wrong   v0.01.0   leading zero"
	note "  right   v0.1.0"
fi

if check_major_suffix "$VERSION"; then
	pass "major version agrees with the module path"
else
	fail "major version conflicts with the module path — see above"
fi

if [ "$(major_of "$VERSION")" = "0" ]; then
	note "v0: the Go API may change between minor versions. See sdk/go/README.md."
fi

# The expensive half — git object reads, tidy, tests, race, coverage, a second
# toolchain — is skipped in --identity mode, which exists to be cheap enough to
# run on every push.
if [ "$MODE" != identity ]; then

# ─── 3. What the release would actually contain ─────────────────────────────
#
# The check this whole script exists for.
#
# A tag is a pointer to a commit, and Go builds the module zip from that commit's
# tree — not from anyone's working directory. A tag whose commit has no
# sdk/go/go.mod does not produce a broken module; it produces NO module, and the
# failure surfaces as an unresolvable version months later in someone else's
# build. So: read the go.mod out of the exact tree being released and check that
# it is the module we think we are publishing.

head2 "Release content"

SOURCE_DESC=""
case "$MODE" in
	worktree)
		SOURCE_DESC="the working tree (files on disk)"
		if [ -f "$SDK_MODULE_DIR/go.mod" ]; then
			CONTENT_MODULE=$(module_path_of "$SDK_MODULE_DIR/go.mod")
			pass "$SDK_MODULE_DIR/go.mod present in the working tree"
		else
			fail "$SDK_MODULE_DIR/go.mod is missing from the working tree"
			CONTENT_MODULE=""
		fi
		;;
	head|tag)
		REF="HEAD"; [ "$MODE" = tag ] && REF="$TAG"
		SOURCE_DESC="commit $(git rev-parse --short "$REF" 2>/dev/null || echo '?') via $REF"

		if [ "$MODE" = tag ] && ! git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
			fail "tag '$TAG' does not exist in this repository"
			exit 1
		fi

		if git cat-file -e "$REF:$SDK_MODULE_DIR/go.mod" 2>/dev/null; then
			CONTENT_MODULE=$(git show "$REF:$SDK_MODULE_DIR/go.mod" | grep -m1 '^module[[:space:]]' | awk '{print $2}')
			pass "$SDK_MODULE_DIR/go.mod exists in $SOURCE_DESC"
		else
			CONTENT_MODULE=""
			fail "$SDK_MODULE_DIR/go.mod does NOT exist in $SOURCE_DESC"
			note "This tag would publish nothing. Go builds the module from the tagged"
			note "COMMIT's tree; files that exist only on disk are not in it."
			note ""
			note "In this repository that is the live situation, not a hypothetical:"
			note "the SDK is currently untracked, so tagging HEAD would create a"
			note "version that resolves to no module for every consumer forever."
		fi
		;;
esac

if [ -n "$CONTENT_MODULE" ]; then
	if [ "$CONTENT_MODULE" = "$SDK_MODULE_PATH" ]; then
		pass "released go.mod declares $CONTENT_MODULE"
	else
		fail "released go.mod declares '$CONTENT_MODULE', not '$SDK_MODULE_PATH'"
		note "The tag prefix and the module path would name different modules."
	fi
fi

# ─── 4. Release preconditions ───────────────────────────────────────────────
#
# Enforced for a real release, reported for a development run. The difference is
# deliberate: this Slice's own work is uncommitted, and a check that could not be
# run until it was committed would be a check nobody ran while writing it.

head2 "Release preconditions"

precondition() { # <ok?> <message>
	if [ "$1" = ok ]; then pass "$2"
	elif [ "$MODE" = worktree ]; then warn "$2  (not enforced in --worktree)"
	else fail "$2"; fi
}

if [ -z "$(git status --porcelain)" ]; then
	precondition ok "working tree is clean"
else
	DIRTY=$(git status --porcelain | wc -l | tr -d ' ')
	precondition no "working tree has $DIRTY uncommitted paths — a tag would not contain them"
fi

if [ "$MODE" != worktree ]; then
	REF="HEAD"; [ "$MODE" = tag ] && REF="$TAG"
	COMMIT=$(git rev-parse "$REF^{commit}" 2>/dev/null)

	# The trust model, and its honest limit.
	#
	# A narrow tag gate must not be able to bless code the branch gate never saw.
	# The cheap mechanism is ancestry: require the released commit to be reachable
	# from the default branch, which is the branch protected by the full CI. It is
	# not proof that CI was green — it is proof the commit went through the place
	# where green is required, which is the part a tag can otherwise skip.
	if git rev-parse -q --verify origin/main >/dev/null 2>&1; then
		if git merge-base --is-ancestor "$COMMIT" origin/main 2>/dev/null; then
			pass "released commit is an ancestor of origin/main"
		else
			fail "released commit is NOT reachable from origin/main"
			note "Tagging a commit that never landed on main lets a narrow release gate"
			note "bless code the full branch CI never ran. Merge first, then tag."
		fi
	else
		warn "origin/main is not available locally — ancestry could not be checked"
		note "Run 'git fetch origin main' before a real release."
	fi

	if [ -n "$(git tag -l "$TAG")" ] && [ "$MODE" = head ]; then
		fail "tag '$TAG' already exists"
		note "Published module versions are immutable. Release $VERSION+1 instead of"
		note "moving this tag; see docs/SDK_GO.md#a-bad-release."
	fi

	# In --tag mode two different trees are being read: the CONTENT checks above
	# read the tag's git objects, while the tests, tidiness and coverage below
	# necessarily run against the checked-out working tree — `go test` cannot run
	# against a git object. If those are different commits, this script would
	# report on a mixture of two releases and call it one.
	#
	# CI checks the tag out, so they agree there. A maintainer running
	# `make sdk-release-verify-tag` from another branch is the case this catches.
	if [ "$MODE" = tag ]; then
		if [ "$(git rev-parse HEAD)" = "$COMMIT" ]; then
			pass "the working tree is checked out at the tagged commit"
		else
			fail "HEAD is not the tagged commit — the tests below would run on different code"
			note "  tagged  $(git rev-parse --short "$COMMIT")"
			note "  HEAD    $(git rev-parse --short HEAD)"
			note "Run 'git checkout $TAG' first."
		fi
	fi
fi

# ─── 5. Module hygiene ──────────────────────────────────────────────────────
#
# These run against the working tree in every mode. In --tag mode CI has checked
# the tag out, so the working tree IS the tagged content.

head2 "Module hygiene"

if [ -d "$SDK_MODULE_DIR" ]; then
	# `go mod tidy` MUTATES, and in --worktree mode it mutates the developer's
	# own files. So the originals are copied byte-for-byte and copied back,
	# rather than captured in a shell variable: `$(cat f)` strips trailing
	# newlines, so restoring from one would "repair" the file into a subtly
	# different one — on the failure path, which is exactly when nobody is
	# looking closely.
	BACKUP=$(mktemp -d "${TMPDIR:-/tmp}/lw-tidy.XXXXXX")
	cp "$SDK_MODULE_DIR/go.mod" "$BACKUP/go.mod"
	HAD_SUM=0
	if [ -f "$SDK_MODULE_DIR/go.sum" ]; then HAD_SUM=1; cp "$SDK_MODULE_DIR/go.sum" "$BACKUP/go.sum"; fi

	restore_module() {
		cp "$BACKUP/go.mod" "$SDK_MODULE_DIR/go.mod"
		if [ "$HAD_SUM" = 1 ]; then
			cp "$BACKUP/go.sum" "$SDK_MODULE_DIR/go.sum"
		else
			rm -f "$SDK_MODULE_DIR/go.sum"
		fi
	}

	if (cd "$SDK_MODULE_DIR" && $GO mod tidy) 2>/dev/null; then
		if cmp -s "$BACKUP/go.mod" "$SDK_MODULE_DIR/go.mod" &&
			{ { [ "$HAD_SUM" = 1 ] && cmp -s "$BACKUP/go.sum" "$SDK_MODULE_DIR/go.sum"; } ||
			  { [ "$HAD_SUM" = 0 ] && [ ! -s "$SDK_MODULE_DIR/go.sum" ]; }; }; then
			pass "go.mod is tidy"
		else
			fail "go mod tidy changed the module definition"
			note "A release must not ship a module definition that disagrees with its"
			note "imports. Run 'cd $SDK_MODULE_DIR && go mod tidy' and commit the result."
		fi
	else
		fail "go mod tidy failed inside $SDK_MODULE_DIR"
	fi
	restore_module
	rm -rf "$BACKUP"

	# Zero dependencies is a product claim, so it is checked rather than trusted —
	# and checked twice, because the two ways of asking have different blind
	# spots. `go mod edit -json` reads the DECLARATION and cannot fail to resolve;
	# `go list -m all` catches what arrives transitively but exits non-zero and
	# silent when a require cannot be downloaded, which would otherwise read as
	# "clean" in exactly the offline case.
	DECLARED=$( (cd "$SDK_MODULE_DIR" && $GO mod edit -json) | python3 -c \
		'import json,sys; print("\n".join(r["Path"]+" "+r["Version"] for r in (json.load(sys.stdin).get("Require") or [])))' 2>/dev/null)
	if [ -n "$DECLARED" ]; then
		fail "$SDK_MODULE_DIR/go.mod declares dependencies:"
		printf '%s\n' "$DECLARED" | sed 's/^/        /'
		note "A dependency here becomes a dependency of every backend that imports"
		note "the SDK, together with its transitive graph and its advisories."
	elif ! RESOLVED=$( (cd "$SDK_MODULE_DIR" && $GO list -m all) 2>&1); then
		fail "go list -m all failed inside $SDK_MODULE_DIR:"
		printf '%s\n' "$RESOLVED" | tail -5 | sed 's/^/        /'
	elif printf '%s\n' "$RESOLVED" | tail -n +2 | grep -q .; then
		fail "the SDK has acquired dependencies:"
		printf '%s\n' "$RESOLVED" | tail -n +2 | sed 's/^/        /'
	else
		pass "no module dependencies"
	fi

	if [ -s "$SDK_MODULE_DIR/go.sum" ]; then
		fail "$SDK_MODULE_DIR/go.sum is non-empty"
	else
		pass "go.sum is absent or empty (zero-dependency)"
	fi
fi

# ─── 6. Public API snapshot ─────────────────────────────────────────────────

head2 "Public API"

API_FILE="$SDK_MODULE_DIR/api.txt"
if [ ! -f "$API_FILE" ]; then
	fail "$API_FILE is missing — run 'make sdk-api-update'"
elif ! ACTUAL=$($GO run scripts/sdk-api-snapshot.go "$SDK_MODULE_DIR" 2>&1); then
	fail "could not compute the API snapshot: $ACTUAL"
else
	if [ "$ACTUAL" = "$(cat "$API_FILE")" ]; then
		pass "exported API matches $API_FILE ($(grep -cvE '^(#|$)' "$API_FILE") declarations)"
	else
		fail "the exported API has changed and $API_FILE was not updated"
		note "This is not automatically wrong — pre-v1 the SDK may break its API — but"
		note "it must be deliberate. Review the diff, then run 'make sdk-api-update'."
		note ""
		diff <(cat "$API_FILE") <(printf '%s\n' "$ACTUAL") | grep -E '^[<>]' | head -25 | sed 's/^/        /'
		note ""
		note "  <  removed or changed   (breaking for anyone already using it)"
		note "  >  added                (safe: a minor release)"
	fi
fi

# ─── 7. The SDK itself ──────────────────────────────────────────────────────

head2 "SDK gates"

run_gate() { # <label> <command...>
	local label="$1"; shift
	if out=$("$@" 2>&1); then
		pass "$label"
	else
		fail "$label"
		printf '%s\n' "$out" | tail -15 | sed 's/^/        /'
	fi
}

run_gate "go vet"                    env -C "$SDK_MODULE_DIR" "$GO" vet ./...
run_gate "go vet -tags acceptance"   env -C "$SDK_MODULE_DIR" "$GO" vet -tags acceptance ./...
run_gate "go test"                   env -C "$SDK_MODULE_DIR" "$GO" test -count=1 ./...
run_gate "go test -race"             env -C "$SDK_MODULE_DIR" "$GO" test -race -count=1 ./...

# The SDK's contract with the SERVER, not with Go.
#
# `go test` above runs the SDK half of the capability gate: every route in
# apicoverage.json is served by a named, exported, scope-documenting method. That
# half cannot notice a route the server ADDED, CHANGED or REMOVED, because it
# only reads the manifest and the SDK.
#
# The other half lives in the root module and checks the same manifest against
# the authorization registry and the OpenAPI document. It needs no database and
# takes about two seconds, and without it an SDK release could be blessed while
# silently no longer covering the API it claims to. Running it here is what
# preserves Slice 13's guarantee across a release.
run_gate "server↔SDK route coverage and OpenAPI drift" \
	"$GO" test -count=1 ./internal/authz/...

if out=$(make -s sdk-coverage-gate 2>&1); then
	pass "$(printf '%s' "$out" | tail -1 | sed 's/^ *+ *//')"
else
	fail "coverage floor"
	printf '%s\n' "$out" | tail -5 | sed 's/^/        /'
fi

# ─── 8. Minimum Go version ──────────────────────────────────────────────────
#
# The go directive is a promise to consumers on older toolchains, and the
# machine's own Go cannot check it: building 1.23-declared code with 1.25 proves
# the LANGUAGE level, not that every stdlib symbol used existed in 1.23.
# GOTOOLCHAIN fetches the real compiler, which is the only thing that does.

head2 "Minimum Go version ($SDK_GO_DIRECTIVE)"

MIN_TOOLCHAIN="go$SDK_GO_DIRECTIVE"
if [ "${SKIP_GO_MIN:-}" = 1 ]; then
	warn "skipped (SKIP_GO_MIN=1)"
elif out=$(cd "$SDK_MODULE_DIR" && GOTOOLCHAIN="$MIN_TOOLCHAIN" $GO build ./... 2>&1); then
	pass "compiles under a real $MIN_TOOLCHAIN toolchain"
	if out=$(cd "$SDK_MODULE_DIR" && GOTOOLCHAIN="$MIN_TOOLCHAIN" $GO test -count=1 ./... 2>&1); then
		pass "tests pass under a real $MIN_TOOLCHAIN toolchain"
	else
		fail "tests fail under $MIN_TOOLCHAIN"
		printf '%s\n' "$out" | tail -10 | sed 's/^/        /'
	fi
else
	fail "does NOT compile under $MIN_TOOLCHAIN, which sdk/go/go.mod promises"
	printf '%s\n' "$out" | tail -10 | sed 's/^/        /'
	note "Either stop using the newer symbol, or raise the go directive — which"
	note "raises the floor for every consumer, so prefer the first."
fi


fi   # end of the --identity skip

# ─── 9. Documentation agreement ─────────────────────────────────────────────
#
# The install command is the one line in this repository whose only reader is
# someone who does not have the repository. Nothing else can catch it being
# wrong, so it is derived here and compared literally.

head2 "Installation docs"

WANT_GET="go get $SDK_MODULE_PATH@"

# Only COPY-PASTEABLE commands are judged: a line naming the full module path is
# something a reader will run, while prose that elides it with `…` is a
# discussion of the command and not the command. Without that distinction this
# check flags the paragraph in docs/SDK_GO.md that exists specifically to explain
# which form is wrong — and a gate that punishes documenting the hazard teaches
# people to stop documenting it.
for doc in "$SDK_MODULE_DIR/README.md" docs/SDK_GO.md README.md CONTRIBUTING.md; do
	[ -f "$doc" ] || continue

	COMMANDS=$(grep -nE "go get [^ ]*$ROOT_MODULE_PATH[^ ]*" "$doc")
	[ -n "$COMMANDS" ] || continue

	doc_ok=1
	while IFS= read -r line; do
		arg=$(printf '%s' "$line" | sed -E 's/.*go get ([^ `]*).*/\1/')
		case "$arg" in
			"$SDK_MODULE_PATH@$SDK_TAG_PREFIX"/*)
				doc_ok=0
				fail "$doc documents the TAG as the version query"
				note "$line"
				note "The tag is '$TAG'; the version a consumer asks for is '$VERSION'."
				note "  wrong   ${WANT_GET}$SDK_TAG_PREFIX/$VERSION"
				note "  right   ${WANT_GET}$VERSION" ;;
			"$SDK_MODULE_PATH@"*)
				v="${arg#"$SDK_MODULE_PATH@"}"
				if [ "$v" != latest ] && ! is_valid_semver "$v"; then
					doc_ok=0
					fail "$doc documents '@$v', which is not a version Go can resolve"
					note "$line"
				fi ;;
			*)
				doc_ok=0
				fail "$doc documents a module path that is not the SDK's"
				note "$line"
				note "  wrong   go get $arg"
				note "  right   ${WANT_GET}$VERSION" ;;
		esac
	done <<-EOF
	$COMMANDS
	EOF

	[ "$doc_ok" = 1 ] && pass "$doc publishes the canonical install command"
done

# ─── Verdict ────────────────────────────────────────────────────────────────

head2 "Verdict"
printf '  checked: %s\n' "$SOURCE_DESC"

if [ "$FAILURES" -eq 0 ]; then
	if [ "$MODE" = identity ]; then
		printf '  \033[32mThe module path, the tag prefix and the documented install command agree.\033[0m\n'
		printf '  This says nothing about whether the code is releasable — that is `make sdk-release-check`.\n'
	elif [ "$MODE" = worktree ]; then
		printf '  \033[32mThe SDK content on disk would make a valid %s release.\033[0m\n' "$VERSION"
		[ "$WARNINGS" -gt 0 ] && printf '  %s release precondition(s) are not met yet — rerun with --head after committing.\n' "$WARNINGS"
	else
		printf '  \033[32m%s is a valid release.\033[0m\n' "$VERSION"
	fi
	exit 0
fi

printf '  \033[31m%s check(s) failed. This would NOT be a valid %s release.\033[0m\n' "$FAILURES" "$VERSION"
exit 1
