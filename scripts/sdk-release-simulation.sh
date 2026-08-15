#!/usr/bin/env bash
#
# sdk-release-simulation.sh — publish the SDK to a throwaway Git remote and
# consume it from outside, to prove the tag format before any real tag exists.
#
# ─── The question this answers ──────────────────────────────────────────────
#
# Does
#
#     module path  +  path-prefixed SemVer tag  +  Go version selection
#
# actually work together? Every other SDK check runs inside the module or with a
# `replace` directive, and neither can fail in the way that matters: a `replace`
# bypasses module resolution completely, so it would pass just as happily if the
# tag format were wrong.
#
# So this one uses no `replace`. It goes through git, through version selection,
# and through the module cache, exactly as a consumer's build does.
#
# ─── Why it snapshots the working tree ──────────────────────────────────────
#
# Git tags point at commits, and the working tree is not the commit. Whenever the
# two differ — an SDK change still unstaged, a changelog edited but not yet
# committed — a simulation built from HEAD would report on content the author is
# not looking at, and one built from the tree would silently claim more than a
# tag could deliver.
#
# It therefore copies the files that a commit WOULD contain (`git ls-files -co
# --exclude-standard`: tracked plus untracked-but-not-ignored), commits them into
# a temporary repository, and tags that. What it proves is a statement about
# *this content*, not about this repository's history — which is the strongest
# honest claim available for a tree that may not yet be committed.
#
# ─── What it does not prove ─────────────────────────────────────────────────
#
# Nothing about proxy.golang.org or sum.golang.org. Resolution here is direct VCS
# with GOPRIVATE set, which deliberately bypasses both. Public proxy resolution
# and the checksum database cannot be tested before a real tag is pushed;
# scripts/first-publish-smoke.sh covers them afterwards.
#
# ─── Safety ────────────────────────────────────────────────────────────────
#
# Every git operation targets a directory under $TMPDIR created by this script.
# The real repository is only ever read, `origin` is never contacted, no tag is
# created in it, and the git configuration used is a private GIT_CONFIG_GLOBAL
# that is deleted with everything else.
#
# Usage:  scripts/sdk-release-simulation.sh [version]        (default v0.1.0)
# Exit:   0 = the tag format resolves for an external consumer · 1 = it does not

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export REPO_ROOT
cd "$REPO_ROOT" || exit 1

# shellcheck source=scripts/lib/sdk-release.sh
. "$REPO_ROOT/scripts/lib/sdk-release.sh"

GO="${GO:-go}"
VERSION="${1:-v0.1.0}"

sdk_release_identity || exit 1
is_valid_semver "$VERSION" || { echo "  ✗ '$VERSION' is not valid SemVer"; exit 1; }

CORRECT_TAG=$(sdk_tag_for "$VERSION")

WORK=$(mktemp -d "${TMPDIR:-/tmp}/lightweight-relsim.XXXXXX")
cleanup() { chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"; }
trap cleanup EXIT

# A private git config, so `url.…insteadOf` — the redirect that makes the public
# module path resolve to a local directory — cannot escape this script or
# outlive it.
export GIT_CONFIG_GLOBAL="$WORK/gitconfig"
export GIT_CONFIG_SYSTEM=/dev/null
: > "$GIT_CONFIG_GLOBAL"
git config --global protocol.file.allow always
git config --global user.email "release-simulation@localhost"
git config --global user.name "release simulation"
git config --global init.defaultBranch main

fails=0
pass() { printf '  + %s\n' "$*"; }
bad()  { printf '  x %s\n' "$*"; fails=$((fails + 1)); }

# ─── 1. Snapshot ────────────────────────────────────────────────────────────

SRC="$WORK/repo"
mkdir -p "$SRC"
git -C "$REPO_ROOT" ls-files -co --exclude-standard -z |
	rsync -a --files-from=- --from0 "$REPO_ROOT/" "$SRC/" || { echo "  ✗ snapshot failed"; exit 1; }

[ -f "$SRC/$SDK_MODULE_DIR/go.mod" ] || { bad "snapshot contains no $SDK_MODULE_DIR/go.mod"; exit 1; }
pass "snapshotted $(git -C "$REPO_ROOT" ls-files -co --exclude-standard | wc -l | tr -d ' ') files into a throwaway repository"

git -C "$SRC" init -q
git -C "$SRC" add -A
git -C "$SRC" commit -q -m "snapshot of the working tree, including $SDK_MODULE_DIR"
pass "committed the snapshot (this content would be the release)"

# ─── 2. Publish to a bare remote, one tag shape at a time ───────────────────

BARE="$WORK/remote.git"
git clone -q --bare "$SRC" "$BARE"

# Redirect the public module path at the throwaway remote. This is what lets the
# consumer ask for the real module path and be served locally, without a replace.
git config --global "url.file://$BARE.insteadOf" "https://$ROOT_MODULE_PATH"

export GOPROXY=direct
export GOSUMDB=off
export GOPRIVATE="$ROOT_MODULE_PATH"
export GONOSUMDB="$ROOT_MODULE_PATH"
export GOFLAGS=-mod=mod
export GIT_TERMINAL_PROMPT=0

retag() { # <tag>
	local t
	for t in $(git --git-dir="$BARE" tag -l); do git --git-dir="$BARE" tag -d "$t" >/dev/null; done
	git --git-dir="$BARE" tag "$1" main
}

# resolve <label> <tag> <query> <expect: ok|fail>
resolve() {
	local label="$1" tag="$2" query="$3" expect="$4"
	retag "$tag"

	local d="$WORK/consumer-$RANDOM$RANDOM"
	mkdir -p "$d"
	cat > "$d/go.mod" <<-EOF
	module example.com/release-consumer

	go $SDK_GO_DIRECTIVE
	EOF

	local out rc
	out=$(cd "$d" && GOMODCACHE="$d/.mc" GOPATH="$d/.gp" "$GO" get "$SDK_MODULE_PATH@$query" 2>&1)
	rc=$?

	if [ "$expect" = ok ]; then
		if [ $rc -eq 0 ]; then
			pass "$label"
		else
			bad "$label — expected it to resolve"
			printf '%s\n' "$out" | grep -v '^go: downloading' | tail -3 | sed 's/^/      /'
		fi
	else
		if [ $rc -ne 0 ]; then
			printf '  + %s\n      rejected: %s\n' "$label" \
				"$(printf '%s' "$out" | grep -v '^go: downloading' | tail -1 | sed "s|$ROOT_MODULE_PATH|…|g" | cut -c1-100)"
		else
			bad "$label — it resolved, and should not have"
		fi
	fi
	chmod -R u+w "$d" 2>/dev/null; rm -rf "$d"
}

echo
echo "  Which tag shape makes 'go get <module>@$VERSION' resolve?"
resolve "tag $CORRECT_TAG"            "$CORRECT_TAG"                   "$VERSION" ok
resolve "tag $VERSION (root-style)"   "$VERSION"                       "$VERSION" fail
resolve "tag sdk/$VERSION"            "sdk/$VERSION"                   "$VERSION" fail
resolve "tag go/$VERSION"             "go/$VERSION"                    "$VERSION" fail
resolve "tag $SDK_TAG_PREFIX/${VERSION#v}" "$SDK_TAG_PREFIX/${VERSION#v}" "$VERSION" fail

# ─── 3. The full external consumer, on the correct tag ──────────────────────

echo
echo "  An external consumer, resolving through the tag with no replace directive:"
retag "$CORRECT_TAG"

APP="$WORK/app"
mkdir -p "$APP"
cat > "$APP/go.mod" <<-EOF
module example.com/external-backend

go $SDK_GO_DIRECTIVE
EOF

cat > "$APP/main.go" <<EOF
package main

import (
	"context"
	"fmt"
	"time"

	lightweight "$SDK_MODULE_PATH"
)

func main() {
	// Printed so the harness can read back which version the BUILD recorded,
	// which is the only way to check that runtime/debug.ReadBuildInfo reports a
	// released dependency's version rather than a placeholder.
	fmt.Println("SDK_VERSION=" + lightweight.Version())

	client, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     "https://identity.example.com",
		WorkspaceID: "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		APIKey:      "lw_sk_0123456789abcdef",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("CLIENT_OK=" + client.WorkspaceID())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = client.Users.List(ctx, lightweight.UserListOptions{Max: 1})
}
EOF

export GOMODCACHE="$APP/.mc" GOPATH="$APP/.gp"

if out=$(cd "$APP" && "$GO" mod tidy 2>&1); then
	pass "go mod tidy resolved the SDK through the tag"
else
	bad "go mod tidy"; printf '%s\n' "$out" | tail -5 | sed 's/^/      /'
fi

if grep -q "^replace" "$APP/go.mod"; then
	bad "the consumer needed a replace directive"
else
	pass "no replace directive in the consumer's go.mod"
fi

if grep -q "$SDK_MODULE_PATH $VERSION" "$APP/go.mod"; then
	pass "go.mod requires $SDK_MODULE_PATH $VERSION"
else
	bad "go.mod does not require the SDK at $VERSION"
	sed 's/^/      /' "$APP/go.mod"
fi

# go.sum must name the SDK and nothing else: the zero-dependency claim, observed
# from the only place it is actually felt.
if [ -f "$APP/go.sum" ]; then
	OTHER=$(grep -v "^$SDK_MODULE_PATH " "$APP/go.sum" | grep -c . )
	if [ "$OTHER" -eq 0 ]; then
		pass "consumer go.sum contains the SDK and nothing else"
	else
		bad "consumer go.sum contains $OTHER line(s) for other modules"
		grep -v "^$SDK_MODULE_PATH " "$APP/go.sum" | sed 's/^/      /'
	fi
fi

if out=$(cd "$APP" && "$GO" build ./... 2>&1); then pass "go build"; else bad "go build"; printf '%s\n' "$out" | tail -8 | sed 's/^/      /'; fi
if out=$(cd "$APP" && "$GO" vet ./... 2>&1);   then pass "go vet";   else bad "go vet";   printf '%s\n' "$out" | tail -8 | sed 's/^/      /'; fi

# The User-Agent question Slice 13 left open, answered from the only vantage
# point that can answer it: a build where the SDK is a released DEPENDENCY.
# Its own test binary always reports "dev", because there the SDK is the main
# module and the toolchain records no version for it.
if out=$(cd "$APP" && "$GO" run . 2>&1); then
	reported=$(printf '%s' "$out" | grep '^SDK_VERSION=' | cut -d= -f2)
	if [ "$reported" = "$VERSION" ]; then
		pass "lightweight.Version() reports $reported from inside a released dependency"
	else
		bad "lightweight.Version() reported '$reported', expected '$VERSION'"
	fi
else
	bad "the consumer program did not run"; printf '%s\n' "$out" | tail -8 | sed 's/^/      /'
fi

# Go includes a repository-root LICENSE in a nested module's zip. Checked rather
# than assumed, because if it did not the module would publish with no license
# and pkg.go.dev would say so on the front page.
MODDIR=$(find "$APP/.mc" -type d -name "go@$VERSION" 2>/dev/null | head -1)
if [ -n "$MODDIR" ] && [ -f "$MODDIR/LICENSE" ]; then
	pass "the published module zip contains LICENSE ($(head -1 "$MODDIR/LICENSE"))"
elif [ -n "$MODDIR" ]; then
	bad "the published module zip has no LICENSE"
else
	bad "could not locate the downloaded module in the cache"
fi

# ─── Verdict ────────────────────────────────────────────────────────────────

echo
if [ "$fails" -eq 0 ]; then
	echo "  + tag '$CORRECT_TAG' publishes $SDK_MODULE_PATH $VERSION to an external consumer,"
	echo "    with no replace directive and no third-party module in its graph."
	echo "    Not proven here: proxy.golang.org and sum.golang.org — see docs/SDK_GO.md."
	exit 0
fi
echo "  ✗ $fails simulation check(s) failed"
exit 1
