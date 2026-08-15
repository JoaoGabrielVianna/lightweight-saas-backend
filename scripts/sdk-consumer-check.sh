#!/usr/bin/env bash
#
# sdk-consumer-check.sh — compile a program that is NOT part of this repository
# against the SDK, and prove it needs nothing else.
#
# ─── What this proves that the SDK's own tests cannot ───────────────────────
#
# `make sdk-test` runs inside sdk/go, where every identifier in the package is
# in scope whether or not it is exported, and where the module's own files are
# always present. A consumer has neither. The interesting failures are therefore
# invisible from inside:
#
#   * an example or README snippet that names something unexported;
#   * a public signature that mentions an internal type a consumer cannot spell;
#   * an accidental import of a server package, which would drag the whole
#     server module — gin, gorm, pgx, Keycloak — into the dependency graph of
#     anything that imports the client;
#   * a go directive claiming a Go version the code does not actually compile
#     under.
#
# So the test is: leave the repository, write a program from the outside, and
# see whether it builds.
#
# ─── replace vs. tag: two different claims ──────────────────────────────────
#
# This script uses a `replace` directive on purpose, and that is a deliberate
# limit rather than an oversight. It answers *"is the SDK's exported surface
# self-sufficient?"* against the working tree, which is what CI needs to check on
# every commit, before any tag exists.
#
# It does NOT answer *"does the published module resolve?"* — a `replace` bypasses
# module resolution entirely, so proving distribution with one would be proving
# nothing. That claim belongs to the tag-resolution simulation
# (scripts/sdk-release-simulation.sh) and, after the first release,
# to scripts/first-publish-smoke.sh. Three questions, three scripts, no
# script overstating what it ran.
#
# Usage:  scripts/sdk-consumer-check.sh
# Exit:   0 = an external module compiles against the SDK · 1 = it does not

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export REPO_ROOT
cd "$REPO_ROOT" || exit 1

# shellcheck source=scripts/lib/sdk-release.sh
. "$REPO_ROOT/scripts/lib/sdk-release.sh"

GO="${GO:-go}"

sdk_release_identity || exit 1

# Outside the repository tree, and not merely in a subdirectory of it: a module
# nested inside this one would inherit context — a parent go.mod, a vendor
# directory, a workspace file — that a real consumer does not have.
WORK=$(mktemp -d "${TMPDIR:-/tmp}/lightweight-sdk-consumer.XXXXXX")

cleanup() {
	# The module cache is written read-only by the go command.
	chmod -R u+w "$WORK" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT

echo "  consumer module: $WORK"

cat > "$WORK/go.mod" <<EOF
module example.com/lightweight-consumer

go $SDK_GO_DIRECTIVE

require $SDK_MODULE_PATH v0.0.0

replace $SDK_MODULE_PATH => $REPO_ROOT/$SDK_MODULE_DIR
EOF

# The smallest program that is still a real integration: construct from the
# environment, make a call, and tell the three error kinds apart. Every symbol
# below is exported API — if any of them stops being, this stops compiling, which
# is the point.
cat > "$WORK/main.go" <<'EOF'
// Command consumer is an external backend using the LIGHTWEIGHT Go SDK.
//
// It lives outside the LIGHTWEIGHT repository on purpose: nothing here may
// depend on anything the SDK does not export.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

func main() {
	// Three environment variables and nowhere to put a fourth:
	// LIGHTWEIGHT_URL, LIGHTWEIGHT_WORKSPACE_ID, LIGHTWEIGHT_API_KEY.
	client, err := lightweight.NewClientFromEnv()
	if err != nil {
		log.Fatalf("configure: %v", err)
	}
	fmt.Println(client.BaseURL(), client.WorkspaceID(), lightweight.Version())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	page, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 50})
	if err != nil {
		// The three error kinds a caller reacts to differently.
		var apiErr *lightweight.APIError
		var reqErr *lightweight.RequestError
		var protoErr *lightweight.ProtocolError
		switch {
		case errors.As(err, &apiErr):
			log.Fatalf("refused: %s (request %s)", apiErr.Code, apiErr.RequestID)
		case errors.As(err, &reqErr):
			log.Fatalf("unreachable: %v", reqErr)
		case errors.As(err, &protoErr):
			log.Fatalf("unreadable answer: %v", protoErr)
		default:
			log.Fatal(err)
		}
	}

	for _, u := range page.Users {
		fmt.Println(u.ID, u.Email)
	}

	// A second service, so the check is not just about Users.
	_, err = client.Audit.List(ctx, lightweight.AuditListOptions{Limit: 10})
	if err != nil {
		log.Fatalf("audit: %v", err)
	}
}
EOF

# A test file too: consumers write tests, and a package that is awkward to test
# from the outside is a package with a design problem.
cat > "$WORK/main_test.go" <<'EOF'
package main

import (
	"testing"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// A consumer must be able to construct a client explicitly, without the
// environment, and must be told clearly when the configuration is wrong.
func TestConfigIsUsableFromOutside(t *testing.T) {
	if _, err := lightweight.NewClient(lightweight.Config{}); err == nil {
		t.Fatal("empty Config was accepted; a consumer would get a confusing failure later")
	}

	c, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     "https://identity.example.com",
		WorkspaceID: "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		APIKey:      "lw_sk_0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("valid Config rejected: %v", err)
	}
	if c.WorkspaceID() == "" || c.BaseURL() == "" {
		t.Fatal("client did not report the workspace it was built for")
	}
}
EOF

export GOFLAGS=-mod=mod
export GOMODCACHE="$WORK/.modcache"
export GOPATH="$WORK/.gopath"

fails=0
step() { # <label> <command...>
	local label="$1"; shift
	if out=$("$@" 2>&1); then
		printf '  + %s\n' "$label"
	else
		printf '  x %s\n' "$label"
		printf '%s\n' "$out" | tail -20 | sed 's/^/      /'
		fails=$((fails + 1))
	fi
}

step "go mod tidy"  env -C "$WORK" "$GO" mod tidy
step "go build"     env -C "$WORK" "$GO" build ./...
step "go vet"       env -C "$WORK" "$GO" vet ./...
step "go test"      env -C "$WORK" "$GO" test -count=1 ./...

# The dependency claim, from the consumer's side. `go list -m all` in the
# consumer names everything that entered its graph; anything beyond the consumer
# itself and the SDK is something an unrelated backend just inherited.
if deps=$(env -C "$WORK" "$GO" list -m all 2>/dev/null | grep -v '^example.com/lightweight-consumer$' | grep -v "^$SDK_MODULE_PATH\b"); then
	if [ -n "$deps" ]; then
		printf '  x importing the SDK pulled in other modules:\n'
		printf '%s\n' "$deps" | sed 's/^/      /'
		printf '      A backend that imports the client did not ask for any of these.\n'
		fails=$((fails + 1))
	fi
fi
[ -z "${deps:-}" ] && printf '  + importing the SDK adds no other module to the consumer graph\n'

# The server-leak check, stated as an import-path rule rather than a dependency
# one: an import of the parent module would be caught above, but naming it here
# makes the failure legible.
if env -C "$WORK" "$GO" list -deps ./... 2>/dev/null | grep -q "^$ROOT_MODULE_PATH/internal"; then
	printf '  x the consumer transitively imports server-internal packages\n'
	fails=$((fails + 1))
else
	printf '  + no server-internal package reaches the consumer\n'
fi

if [ "$fails" -eq 0 ]; then
	echo "  + an external module compiles, vets and tests against the SDK alone"
	exit 0
fi
echo "  ✗ $fails consumer-boundary check(s) failed"
exit 1
