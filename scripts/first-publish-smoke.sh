#!/usr/bin/env bash
#
# first-publish-smoke.sh — after a real tag is pushed, check that the public
# infrastructure actually serves it.
#
# ─── Why this is a separate script ──────────────────────────────────────────
#
# scripts/sdk-release-simulation.sh proves the tag format against a local Git
# remote with GOPRIVATE set, which bypasses proxy.golang.org and sum.golang.org
# by design. That is the strongest proof available BEFORE publishing, and it is
# silent about the three things that only exist afterwards:
#
#   1. the module proxy has fetched and cached the version;
#   2. the checksum database has recorded its hash;
#   3. pkg.go.dev has indexed and rendered the documentation.
#
# Those are not code problems, so they cannot be fixed by a code change and are
# not worth guessing at. They are observations, made once, after step 5 of the
# release checklist in docs/SDK_GO.md.
#
# This script forces the public path: GOPROXY is the real proxy, GOSUMDB is the
# real checksum database, and GOPRIVATE/GONOSUMDB/GOFLAGS are cleared so a
# developer's own environment cannot accidentally make a failure look like a
# success. It runs in a temporary directory outside the repository.
#
# It reads only, and needs network access.
#
# Usage:  scripts/first-publish-smoke.sh v0.1.0
# Exit:   0 = publicly resolvable · 1 = not (yet)

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export REPO_ROOT
cd "$REPO_ROOT" || exit 1

# shellcheck source=scripts/lib/sdk-release.sh
. "$REPO_ROOT/scripts/lib/sdk-release.sh"

GO="${GO:-go}"
VERSION="${1:-}"

sdk_release_identity || exit 1

if [ -z "$VERSION" ]; then
	echo "usage: scripts/first-publish-smoke.sh <version>   e.g. v0.1.0"
	exit 2
fi
if ! is_valid_semver "$VERSION"; then
	echo "  ✗ '$VERSION' is not valid SemVer"
	exit 1
fi

TAG=$(sdk_tag_for "$VERSION")

# Refuse to run against a version that was never published. Without this the
# script's most likely use is the one where its failure means nothing — run too
# early, out of eagerness, and then discounted.
if ! git ls-remote --tags origin "refs/tags/$TAG" 2>/dev/null | grep -q .; then
	cat <<-EOF
	  ✗ tag '$TAG' has not been pushed to origin.

	    There is nothing for the proxy to serve, so this script would report a
	    failure that says nothing about the release. It is meant to be run AFTER
	    step 5 of the release checklist in docs/SDK_GO.md.

	    To check the mechanics before publishing, use the offline simulation:
	        make sdk-release-simulate VERSION=$VERSION
	EOF
	exit 1
fi
echo "  + tag '$TAG' exists on origin"

WORK=$(mktemp -d "${TMPDIR:-/tmp}/lightweight-smoke.XXXXXX")
cleanup() { chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"; }
trap cleanup EXIT

# The public path, forced. Anything a developer has set locally to make private
# modules work would otherwise silently change what is being tested.
export GOPROXY=https://proxy.golang.org,direct
export GOSUMDB=sum.golang.org
export GOMODCACHE="$WORK/.modcache"
export GOPATH="$WORK/.gopath"
unset GOPRIVATE GONOSUMDB GONOSUMCHECK GOFLAGS GONOPROXY GOINSECURE

fails=0
pass() { printf '  + %s\n' "$*"; }
bad()  { printf '  x %s\n' "$*"; fails=$((fails + 1)); }

APP="$WORK/app"
mkdir -p "$APP"
cat > "$APP/go.mod" <<-EOF
module example.com/smoke

go $SDK_GO_DIRECTIVE
EOF

cat > "$APP/main.go" <<EOF
package main

import (
	"fmt"

	lightweight "$SDK_MODULE_PATH"
)

func main() {
	fmt.Println("SDK_VERSION=" + lightweight.Version())
	if _, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     "https://identity.example.com",
		WorkspaceID: "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		APIKey:      "lw_sk_0123456789abcdef",
	}); err != nil {
		panic(err)
	}
	fmt.Println("CLIENT_OK")
}
EOF

echo "  smoke module: $APP  (outside the repository, public proxy, checksum db on)"

# 1. The documented command, exactly as a consumer would type it.
if out=$(cd "$APP" && $GO get "$SDK_MODULE_PATH@$VERSION" 2>&1); then
	pass "go get $SDK_MODULE_PATH@$VERSION"
else
	bad "go get $SDK_MODULE_PATH@$VERSION"
	printf '%s\n' "$out" | tail -6 | sed 's/^/      /'
	printf '      The proxy can take a few minutes to observe a new tag; if the error\n'
	printf '      mentions the checksum database, do NOT retag — see docs/SDK_GO.md.\n'
fi

# 2. The checksum database recorded it. GONOSUMDB is unset above, so a go.sum
#    entry here means sum.golang.org answered.
if [ -f "$APP/go.sum" ] && grep -q "^$SDK_MODULE_PATH $VERSION" "$APP/go.sum"; then
	pass "sum.golang.org recorded a hash for $VERSION"
else
	bad "no checksum-database entry for $VERSION"
fi

# 3. Zero dependencies, observed publicly rather than locally.
if [ -f "$APP/go.sum" ]; then
	OTHER=$(grep -cv "^$SDK_MODULE_PATH " "$APP/go.sum")
	[ "$OTHER" -eq 0 ] && pass "the published module still pulls in nothing else" ||
		bad "$OTHER go.sum line(s) for other modules"
fi

for step in "mod tidy" "build ./..." "vet ./..."; do
	# shellcheck disable=SC2086
	if out=$(cd "$APP" && $GO $step 2>&1); then pass "go $step"; else bad "go $step"; printf '%s\n' "$out" | tail -6 | sed 's/^/      /'; fi
done

if out=$(cd "$APP" && $GO run . 2>&1); then
	reported=$(printf '%s' "$out" | grep '^SDK_VERSION=' | cut -d= -f2)
	[ "$reported" = "$VERSION" ] &&
		pass "lightweight.Version() reports $reported (User-Agent: lightweight-go/$reported)" ||
		bad "Version() reported '$reported', expected '$VERSION'"
else
	bad "the smoke program did not run"
fi

# 4. pkg.go.dev. Indexing lags the proxy by minutes to hours, so a miss here is
#    reported as an observation rather than a failure — the release is already
#    usable without it.
DOC_URL="https://pkg.go.dev/$SDK_MODULE_PATH@$VERSION"
if command -v curl >/dev/null; then
	code=$(curl -sS -o /dev/null -w '%{http_code}' -L --max-time 20 "$DOC_URL" 2>/dev/null)
	case "$code" in
		200) pass "pkg.go.dev is serving $DOC_URL" ;;
		404) printf '  ! pkg.go.dev has not indexed %s yet (HTTP 404)\n' "$VERSION"
		     printf '      Normal within the first hour. Requesting the URL once triggers indexing.\n' ;;
		*)   printf '  ! pkg.go.dev returned HTTP %s for %s\n' "$code" "$DOC_URL" ;;
	esac
fi

echo
if [ "$fails" -eq 0 ]; then
	echo "  + $SDK_MODULE_PATH@$VERSION is publicly installable."
	exit 0
fi
echo "  ✗ $fails public-resolution check(s) failed"
exit 1
