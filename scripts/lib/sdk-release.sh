# sdk-release.sh — the one place that knows how the SDK's module path, its
# release tag and its documented install command relate. Sourced, never
# executed.
#
# ─── Why this file exists ───────────────────────────────────────────────────
#
# The string `sdk/go` wants to exist in at least five places: the directory, the
# module path in sdk/go/go.mod, the tag prefix Go requires for a nested module,
# the workflow's tag filter, and the `go get` line in two READMEs. Five copies of
# one fact is five chances for four of them to stay right while the fifth quietly
# stops being true — and the one that goes stale is the README, which is the only
# copy a consumer ever reads.
#
# So there is exactly ONE literal here — the directory — and everything else is
# DERIVED from the go.mod files and checked against them. `sdk_release_identity`
# fails if the derivation and the declaration disagree, which is what makes
# renaming the module a loud event instead of a silent one.
#
# ─── The distinction this file exists to keep straight ──────────────────────
#
# A nested module has TWO different strings that both look like a version, and
# conflating them is the mistake this whole file is arranged to prevent:
#
#   the git TAG            sdk/go/v0.1.0     what you `git tag`
#   the module VERSION     v0.1.0            what you `go get …@<here>`
#
# Go derives one from the other. A consumer never types the tag. Before Slice 16
# both READMEs documented `go get …/sdk/go@sdk/go/v0.1.0`, which resolves only
# because Go also accepts a bare revision name as a query — it is not the
# canonical form, it drags the parent module into resolution, and it is not what
# the proxy or pkg.go.dev will show anyone.
#
# ─── Contract ───────────────────────────────────────────────────────────────
#
# Source with the repository root as the working directory, or set REPO_ROOT.
# After sourcing, call `sdk_release_identity` once; it sets and validates:
#
#   SDK_MODULE_DIR     sdk/go        the single literal
#   ROOT_MODULE_PATH   github.com/…/lightweight-saas-backend
#   SDK_MODULE_PATH    github.com/…/lightweight-saas-backend/sdk/go
#   SDK_TAG_PREFIX     sdk/go        derived; the prefix Go requires on the tag
#   SDK_GO_DIRECTIVE   1.23.0        the SDK's minimum Go, from its own go.mod

# The one literal. Everything below derives from it or checks against it.
SDK_MODULE_DIR="sdk/go"

# module_path_of <go.mod path> — the module line, without the `module ` keyword.
#
# Reads the FIRST `module` line at column zero. sdk/go/go.mod opens with a long
# comment block that contains the word "module" in prose, so a bare grep for
# `module` finds the wrong line; anchoring to column zero is what makes this
# right rather than lucky.
module_path_of() {
	local gomod="$1"
	[ -f "$gomod" ] || { echo "sdk-release: no such go.mod: $gomod" >&2; return 1; }
	local path
	path=$(grep -m1 '^module[[:space:]]' "$gomod" | awk '{print $2}')
	[ -n "$path" ] || { echo "sdk-release: no module directive in $gomod" >&2; return 1; }
	printf '%s\n' "$path"
}

# go_directive_of <go.mod path> — the `go` directive, e.g. 1.23.0.
go_directive_of() {
	local gomod="$1"
	grep -m1 '^go[[:space:]]' "$gomod" | awk '{print $2}'
}

# sdk_release_identity — derive the release identity and prove it is coherent.
#
# The check that earns its keep is the last one: SDK_MODULE_PATH must equal
# ROOT_MODULE_PATH + "/" + SDK_MODULE_DIR. Go derives the tag prefix from exactly
# that relationship, so if someone renames the module inside sdk/go/go.mod
# without moving the directory, the tag prefix this file reports would become
# wrong — and every release built on it would produce a version no consumer can
# resolve. Failing here is the only way that mistake is cheap.
sdk_release_identity() {
	local root="${REPO_ROOT:-.}"

	ROOT_MODULE_PATH=$(module_path_of "$root/go.mod") || return 1
	SDK_MODULE_PATH=$(module_path_of "$root/$SDK_MODULE_DIR/go.mod") || return 1
	SDK_GO_DIRECTIVE=$(go_directive_of "$root/$SDK_MODULE_DIR/go.mod")

	local expected="$ROOT_MODULE_PATH/$SDK_MODULE_DIR"
	if [ "$SDK_MODULE_PATH" != "$expected" ]; then
		cat >&2 <<-EOF
		  ✗ the SDK module path does not match its location in the repository

		      declared in $SDK_MODULE_DIR/go.mod : $SDK_MODULE_PATH
		      required by its directory          : $expected

		    Go derives a nested module's release tag prefix from this relationship.
		    While they disagree, no tag can make this module resolvable: the prefix
		    Go looks for is its directory, and the path it publishes is the module
		    line. Either move the directory or fix the module line.
		EOF
		return 1
	fi

	# Derived, not declared. This is the assignment that removes the fourth copy.
	SDK_TAG_PREFIX="$SDK_MODULE_DIR"
	return 0
}

# sdk_tag_for <version> — the git tag that publishes <version> of the SDK.
#
#   sdk_tag_for v0.1.0  ->  sdk/go/v0.1.0
sdk_tag_for() { printf '%s/%s\n' "$SDK_TAG_PREFIX" "$1"; }

# sdk_version_of_tag <tag> — the module version a tag publishes, or empty if the
# tag is not an SDK release tag at all.
#
#   sdk_version_of_tag sdk/go/v0.1.0  ->  v0.1.0
#   sdk_version_of_tag v0.1.0         ->  (empty)
sdk_version_of_tag() {
	local tag="$1"
	case "$tag" in
		"$SDK_TAG_PREFIX"/*) printf '%s\n' "${tag#"$SDK_TAG_PREFIX"/}" ;;
		*) return 1 ;;
	esac
}

# sdk_install_command <version> — the command the docs must publish.
#
# Note what is NOT here: the tag. A consumer queries the VERSION; the tag is an
# implementation detail of how the maintainer publishes it.
sdk_install_command() { printf 'go get %s@%s\n' "$SDK_MODULE_PATH" "$1"; }

# is_valid_semver <version> — strict SemVer 2.0.0 with the Go-required `v`.
#
# Deliberately strict about leading zeros and a missing patch component. Go
# itself rejects `v0.1` and `v0.01.0` as module versions, so accepting them here
# would only move the failure from a script the maintainer is watching to a tag
# that is already pushed.
is_valid_semver() {
	printf '%s' "$1" | grep -qE '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
}

# major_of <version> — the numeric major, e.g. v2.3.4 -> 2.
major_of() { printf '%s' "$1" | sed -E 's/^v([0-9]+)\..*/\1/'; }

# check_major_suffix <version> — the /vN rule.
#
# Go's import compatibility rule: v0 and v1 use the bare module path, and v2 and
# beyond must carry a `/vN` suffix ON THE MODULE PATH ITSELF. Tagging v2.0.0
# against a path with no suffix produces a tag the go command will not select —
# it is not an error anywhere, it simply never resolves, which is the worst
# possible failure mode for a release. Checking it costs four lines.
check_major_suffix() {
	local version="$1" major
	major=$(major_of "$version")

	if [ "$major" -le 1 ]; then
		case "$SDK_MODULE_PATH" in
			*/v[0-9]|*/v[0-9][0-9])
				echo "  ✗ $version is a v0/v1 release but the module path carries a major suffix: $SDK_MODULE_PATH" >&2
				return 1 ;;
		esac
		return 0
	fi

	case "$SDK_MODULE_PATH" in
		*"/v$major")
			return 0 ;;
		*)
			cat >&2 <<-EOF
			  ✗ $version needs a major-version suffix on the module path

			      module path : $SDK_MODULE_PATH
			      required    : $SDK_MODULE_PATH/v$major

			    Go's import compatibility rule: v2 and later are a different module.
			    A v$major tag on an unsuffixed path does not fail — it simply never
			    resolves for anyone, which is why this is checked before the tag exists.

			    Releasing v$major means creating $SDK_MODULE_DIR/v$major/ or updating the
			    module line, and every consumer changes their import path.
			EOF
			return 1 ;;
	esac
}
