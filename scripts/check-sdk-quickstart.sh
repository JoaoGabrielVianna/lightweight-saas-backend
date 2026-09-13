#!/usr/bin/env bash
#
# check-sdk-quickstart.sh — run the SDK's documented install instructions as a
# consumer would, against the public module proxy, and fail if they do not work.
#
# ─── Why this exists ────────────────────────────────────────────────────────
#
# v0.4.2 changed no production code. It corrected documentation that had been
# wrong for two releases, the worst of it in sdk/go/README.md: the install
# section told the reader that v0.1.0 "does not exist on GitHub yet, so running
# it today fails". The module had been published and resolvable for nine days.
# The documentation was telling a user not to do something that worked.
#
# No gate caught it, and the reason is structural. The gated half of the
# documentation — route counts, scope counts, links — was always right. The
# prose half described a product two releases old. Prose that no gate reads is a
# comment, and comments rot.
#
# This gate closes the part of the prose that is ACTIONABLE: the instructions a
# reader copies and runs. It does not, and cannot, judge descriptive prose. That
# limitation is deliberate and recorded in docs/QUALITY_GATE.md § Documentation.
#
# ─── The property that makes it worth having ────────────────────────────────
#
# Everything executed here is EXTRACTED from the document. Nothing is written
# into this script. The module path, the version, the import line and the alias
# all come out of the Markdown at run time.
#
# That is the whole design. A gate that hard-coded the command would test what
# the script believes, leave the document free to say something else, and report
# green either way — the same trap as a stub built from the structs it is meant
# to check: it confirms the expectation rather than the reality.
#
# The one thing NOT extracted is the symbol the smoke program calls, `Version`.
# An import alone does not prove a package is usable, and Go has no syntax that
# references an identifier without knowing whether it is a type or a function,
# so the symbols a document mentions cannot be compiled generically. They are
# checked for existence instead, with `go doc`, in step 5.
#
# ─── Which documents, and the guard on that list ────────────────────────────
#
# Three of them publish the command as an instruction, and all three are gated:
# README.md, sdk/go/README.md and docs/getting-started/CONNECT_BACKEND.md. The
# last one is the reason the list is not a constant nobody revisits: it is the
# SDK's own getting-started guide, it carries the same `go get` and the same
# import, and until this gate went in NOTHING read it — not even the offline
# identity check, whose own list of documents predates the file.
#
# So `check_gate_coverage` greps every tracked Markdown file for the command and
# fails if one is in neither GATED_DOCS nor EXEMPT_DOCS. A document publishing an
# install instruction that no gate reads is the exact shape of the v0.4.2 bug,
# and it must not be possible to create one silently.
#
# ─── What is checked, and what each step would have caught ──────────────────
#
#   0. the gated list is complete         a new document publishes `go get` and
#                                         nothing reads it
#   1. the document is extractable        the install block was reformatted away
#   2. paths agree, version is a version  `go get` and `import` drifted apart
#   3. `go get <extracted>` resolves      THE v0.4.2 BUG — a document cites a
#                                         version the proxy does not serve
#   4. the extracted import compiles      wrong module path, or a package that
#                                         does not build for a consumer
#   5. the documented alias is the real   the document teaches a selector that is
#      package name, and every symbol     not the package's name, or names a
#      the go examples name exists        symbol the published version lacks
#   6. Version() matches the document     a version other than the published one
#
# ─── The environment, and why it is scrubbed ────────────────────────────────
#
# Same approach as scripts/first-publish-smoke.sh, for the same reason: the
# public path is the only path worth testing here. A `replace` directive, a
# GOPRIVATE covering this repository, or a GOFLAGS set for a private proxy would
# each make this gate pass while the actual consumer breaks. It runs in a
# temporary module outside the repository, with the real proxy and the real
# checksum database, and clears everything a developer might have set.
#
# It reads only. It writes nothing to the repository, and in particular it never
# edits the documents it checks: this gate reports divergence and exits non-zero.
# A document that corrects itself hides that it was ever wrong, and the
# correction lands without anyone deciding it was the right one.
#
# ─── It needs the network ───────────────────────────────────────────────────
#
# That is the cost, and it was accepted deliberately rather than worked around.
# See docs/QUALITY_GATE.md § Releasing the Go SDK for the decision and how to
# reverse it.
#
# Usage:
#   scripts/check-sdk-quickstart.sh                  # every gated document
#   scripts/check-sdk-quickstart.sh path/to/DOC.md   # just this one
#   scripts/check-sdk-quickstart.sh --extract [path] # parse only, no network
#   scripts/check-sdk-quickstart.sh --self-test      # prove the gate still bites
#
# Exit: 0 = every documented quickstart works as written · 1 = one does not ·
#       2 = usage

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export REPO_ROOT
cd "$REPO_ROOT" || exit 1

# shellcheck source=scripts/lib/sdk-release.sh
. "$REPO_ROOT/scripts/lib/sdk-release.sh"

GO="${GO:-go}"

# One temporary root for the whole run, removed on exit. Every throwaway module
# and every self-test fixture lives under it, so there is exactly one cleanup
# path rather than one per early return.
WORKROOT=$(mktemp -d "${TMPDIR:-/tmp}/lightweight-quickstart.XXXXXX")
cleanup() { chmod -R u+w "$WORKROOT" 2>/dev/null; rm -rf "$WORKROOT"; }
trap cleanup EXIT

# Derived once, before anything reads it: the extractor matches the install
# command against ROOT_MODULE_PATH, and the coverage guard greps for it.
sdk_release_identity || exit 1

FAILURES=0
pass() { printf '  \033[32m+\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mx\033[0m %s\n' "$*"; FAILURES=$((FAILURES + 1)); }
note() { printf '      %s\n' "$*"; }
head2() { printf '\n\033[1m%s\033[0m\n' "$*"; }

usage() {
	sed -n '/^# Usage:/,/^# Exit:/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
	exit 2
}

# ─── The extractor ──────────────────────────────────────────────────────────
#
# Reads the install section of a Markdown file and prints TAB-separated
# key/value pairs for everything downstream needs. Prints nothing for a field it
# could not find, which is how a reformatted README becomes a failure rather
# than a silent skip.
#
# ─── What it anchors on, and why not on a heading ───────────────────────────
#
# The pair is: the first fenced `go get` for this module, and the import of the
# same module in the first ```go block AFTER it. That is the order a reader
# meets them in, and it is the only structure the three gated documents share.
#
# Anchoring on a `## Install` heading was the first attempt and it was wrong.
# The root README introduces the same command under `**Go:**` with no heading at
# all, and docs/getting-started/CONNECT_BACKEND.md puts the import two headings
# below the install block, under `### First call`. A heading-anchored extractor
# reports "nothing to check" on both — which is the one answer this gate must
# never give quietly.
#
# Three rules keep it from reading the wrong lines:
#
#   * fences are tracked first, so a `#` comment inside a bash block is never
#     mistaken for a heading, and prose is never mistaken for code;
#   * the `go get` must be the FIRST TOKEN of a line inside a fence. That is
#     what separates a command from a transcript of one: docs/SDK_GO.md shows
#     `+ go get …` as captured script output, which is a report and not an
#     instruction;
#   * the import is matched by MODULE PATH, not by position, so the stdlib
#     imports sharing a grouped block with it are ignored.
#
# Both import forms are read, because both are published:
#
#     import lightweight "…/sdk/go"      sdk/go/README.md, README.md
#     import (                            docs/getting-started/CONNECT_BACKEND.md
#         lightweight "…/sdk/go"
#     )
extract_quickstart() {
	awk -v modroot="$ROOT_MODULE_PATH" '
	function strip(s) { sub(/^[[:space:]]+/, "", s); sub(/[[:space:]]+$/, "", s); return s }
	function unquote(s) { gsub(/"/, "", s); return s }
	# An import of THIS REPOSITORY but not of the module the go get names. Kept
	# so the gate can say "these two lines disagree" and point at both, instead
	# of the much less useful "no import found".
	function remember_mismatch(ln, raw, p) {
		if (mism_line != "" || index(p, modroot) == 0) return
		mism_line = ln; mism_raw = raw; mism_path = p
	}
	BEGIN { infence = 0; lang = ""; got = 0; getfence = 0; fence = 0; ingroup = 0; done = 0 }
	{
		line = $0

		if (line ~ /^[[:space:]]*```/) {
			if (infence) { infence = 0; lang = ""; ingroup = 0 }
			else {
				infence = 1
				fence++
				lang = line
				sub(/^[[:space:]]*```[[:space:]]*/, "", lang)
				lang = strip(lang)
			}
			next
		}

		if (!infence) next

		# Every ```go block is a promise the reader may compile, so symbols are
		# collected from all of them, wherever they sit in the document.
		if (lang == "go") {
			s = line
			while (match(s, /[A-Za-z_][A-Za-z0-9_]*\.[A-Z][A-Za-z0-9_]*/)) {
				ref = substr(s, RSTART, RLENGTH)
				s = substr(s, RSTART + RLENGTH)
				split(ref, q, ".")
				print "symbol\t" q[1] "\t" q[2]
			}
		}

		# 1. The install command: first token of the line, naming this repository.
		if (!got && line ~ /^[[:space:]]*go get[[:space:]]/ && index(line, modroot) > 0) {
			cmd = strip(line)
			split(cmd, g, /[[:space:]]+/)
			arg = g[3]
			gsub(/[`'"'"']/, "", arg)
			arg = unquote(arg)
			print "get_line\t" NR
			print "get_cmd\t" cmd
			print "get_arg\t" arg
			got = 1
			getfence = fence
			split(arg, a, "@")
			wantpath = a[1]
			next
		}

		# 2. The import of the SAME module, in a later ```go block.
		if (done || !got || lang != "go" || fence <= getfence) next

		if (line ~ /^[[:space:]]*import[[:space:]]*\([[:space:]]*$/) { ingroup = 1; next }
		if (ingroup && line ~ /^[[:space:]]*\)[[:space:]]*$/) { ingroup = 0; next }

		if (line ~ /^[[:space:]]*import[[:space:]]+[A-Za-z_][A-Za-z0-9_]*[[:space:]]+"[^"]+"[[:space:]]*$/) {
			imp = strip(line)
			split(imp, f, /[[:space:]]+/)
			if (unquote(f[3]) != wantpath) { remember_mismatch(NR, imp, unquote(f[3])); next }
			print "import_line\t" NR
			print "import_raw\t" imp
			print "import_alias\t" f[2]
			print "import_path\t" unquote(f[3])
			print "import_grouped\t0"
			done = 1
			next
		}

		if (ingroup && line ~ /^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]+"[^"]+"[[:space:]]*$/) {
			spec = strip(line)
			split(spec, f, /[[:space:]]+/)
			if (unquote(f[2]) != wantpath) { remember_mismatch(NR, spec, unquote(f[2])); next }
			print "import_line\t" NR
			print "import_raw\t" spec
			print "import_alias\t" f[1]
			print "import_path\t" unquote(f[2])
			print "import_grouped\t1"
			done = 1
			next
		}

		# An import of this module with no alias at all. Recorded so the gate can
		# say which line is at fault instead of reporting nothing found.
		if (line ~ /^[[:space:]]*(import[[:space:]]+)?"[^"]+"[[:space:]]*$/) {
			spec = strip(line)
			p = spec; sub(/^import[[:space:]]+/, "", p)
			if (unquote(p) != wantpath) next
			print "import_line\t" NR
			print "import_raw\t" spec
			print "import_grouped\t" ingroup
			done = 1
		}
	}
	END {
		if (!done && mism_line != "") {
			print "import_mismatch_line\t" mism_line
			print "import_mismatch_raw\t" mism_raw
			print "import_mismatch_path\t" mism_path
		}
	}
	' "$1"
}

# field <key> — one value out of the extractor output held in $PARSED.
field() { printf '%s\n' "$PARSED" | awk -F'\t' -v k="$1" '$1 == k { print $2; exit }'; }

# ─── The gate ───────────────────────────────────────────────────────────────

check_readme() {
	local readme="$1"

	sdk_release_identity || return 1

	head2 "Extracting the quickstart from $readme"

	if [ ! -f "$readme" ]; then
		fail "no such file: $readme"
		return 1
	fi

	PARSED=$(extract_quickstart "$readme")

	local get_cmd get_arg get_line import_raw import_line alias import_path grouped
	get_cmd=$(field get_cmd)
	get_arg=$(field get_arg)
	get_line=$(field get_line)
	import_raw=$(field import_raw)
	import_line=$(field import_line)
	alias=$(field import_alias)
	import_path=$(field import_path)
	grouped=$(field import_grouped)

	# 1. Extractable at all. A document that cannot be parsed is a document that
	#    cannot be checked, and "cannot be checked" must never read as "fine".
	if [ -z "$get_cmd" ]; then
		fail "no install command found in $readme"
		note "Expected, as the first token of a line inside a fenced block:"
		note "    $(sdk_install_command v0.1.0)"
		note "This gate runs what the document says. If it stops saying it in a form"
		note "a machine can read, nothing is being checked — so this fails rather"
		note "than passing quietly."
		return 1
	fi
	pass "install command, $readme:$get_line — $get_cmd"

	if [ -z "$import_raw" ]; then
		local mism_line mism_raw mism_path
		mism_line=$(field import_mismatch_line)
		mism_raw=$(field import_mismatch_raw)
		mism_path=$(field import_mismatch_path)

		if [ -n "$mism_raw" ]; then
			fail "the install command and the import name different modules"
			note "  $readme:$get_line   $get_cmd"
			note "  $readme:$mism_line   $mism_raw"
			note "  fetched : ${get_arg%@*}"
			note "  imported: $mism_path"
			note "A reader who runs the first and writes the second gets a build error."
			return 1
		fi

		fail "$readme publishes an install command with no import beside it"
		note "$readme:$get_line — $get_cmd"
		note "Expected, in a \`\`\`go block after it, one of:"
		note "    import lightweight \"${get_arg%@*}\""
		note "    import (  …  lightweight \"${get_arg%@*}\"  )"
		note "A reader told to fetch a module and not told how to name it has to"
		note "guess the selector, and the path's last element is 'go'."
		return 1
	fi
	if [ -z "$alias" ]; then
		fail "$readme:$import_line — the import carries no explicit alias"
		note "$import_raw"
		note "The alias is the point of documenting this import at all: the path's"
		note "last element is 'go' and the package is named 'lightweight', so a"
		note "reader who does not see the alias has to guess the selector."
		return 1
	fi
	pass "import, $readme:$import_line — $import_raw$([ "$grouped" = 1 ] && echo '  (grouped)')"

	# 2. Internal and external agreement, before spending a network round trip.
	local readme_path readme_version
	case "$get_arg" in
		*@*) readme_path="${get_arg%@*}"; readme_version="${get_arg##*@}" ;;
		*)   readme_path="$get_arg";      readme_version="" ;;
	esac

	head2 "Agreement"

	# The two naming the same module is structural rather than checked here: the
	# extractor matches the import BY the path the go get names, so a document
	# where they diverge produces no import at all and is reported above, with
	# both line numbers. This is the assertion that the structure held.
	if [ "$readme_path" != "$import_path" ]; then
		fail "internal error: extracted an import for a different module"
		note "  go get : $readme_path"
		note "  import : $import_path"
		return 1
	fi
	pass "the install command and the import name the same module"

	if [ "$readme_path" != "$SDK_MODULE_PATH" ]; then
		fail "$readme documents a module path this repository does not publish"
		note "  documented : $readme_path"
		note "  declared in $SDK_MODULE_DIR/go.mod : $SDK_MODULE_PATH"
		note "Nothing the proxy serves will make the documented path resolve."
		return 1
	fi
	pass "the documented path is the one $SDK_MODULE_DIR/go.mod declares"

	if [ -z "$readme_version" ]; then
		fail "$readme:$get_line — the install command names no version"
		note "$get_cmd"
		note "Without @<version> the reader gets whatever is latest at the time,"
		note "which is not what the rest of this README describes."
		return 1
	fi
	if [ "$readme_version" != latest ] && ! is_valid_semver "$readme_version"; then
		fail "$readme:$get_line — '@$readme_version' is not a version Go can resolve"
		note "$get_cmd"
		if [ "$readme_version" != "${readme_version#"$SDK_TAG_PREFIX"/}" ]; then
			note "That is the TAG. The version a consumer asks for is the part after"
			note "'$SDK_TAG_PREFIX/'; Go derives the tag from the module's directory."
		fi
		return 1
	fi
	pass "the documented version is well-formed: @$readme_version"

	if [ "${EXTRACT_ONLY:-0}" = 1 ]; then
		head2 "Parsed"
		printf '  %-14s %s\n' "module path" "$readme_path"
		printf '  %-14s %s\n' "version" "$readme_version"
		printf '  %-14s %s\n' "alias" "$alias"
		printf '  %-14s %s\n' "install" "$get_cmd"
		printf '  %-14s %s\n' "import" "$import_raw"
		return 0
	fi

	# ─── The public path ────────────────────────────────────────────────────

	local work
	work=$(mktemp -d "$WORKROOT/run.XXXXXX")

	# Copied from first-publish-smoke.sh on purpose: the consumer's environment
	# is the thing under test, so the developer's must not leak into it.
	export GOPROXY=https://proxy.golang.org,direct
	export GOSUMDB=sum.golang.org
	export GOMODCACHE="$work/.modcache"
	export GOPATH="$work/.gopath"
	unset GOPRIVATE GONOSUMDB GONOSUMCHECK GOFLAGS GONOPROXY GOINSECURE

	local app="$work/app"
	mkdir -p "$app"
	cat > "$app/go.mod" <<-EOF
	module example.com/readme-quickstart

	go $SDK_GO_DIRECTIVE
	EOF

	head2 "The documented command, run outside the repository"
	note "$app — public proxy, checksum database on, no replace directive"

	# 3. THE check. The argument is the extracted string, so a README that cites
	#    an unpublished version fails right here.
	#
	#    The line is not `eval`ed. It was matched against `^go get ` above, so
	#    the verb is already known; what can be wrong — the module path and the
	#    version — is the part that comes from the document. Running a shell
	#    string lifted out of a Markdown file would hand code execution to
	#    anyone who can open a pull request, which is a steep price for testing
	#    a verb this script just finished asserting.
	local out
	if out=$(cd "$app" && $GO get "$get_arg" 2>&1); then
		pass "$get_cmd"
	else
		fail "$readme:$get_line — the documented install command does not work"
		note "$get_cmd"
		printf '%s\n' "$out" | tail -6 | sed 's/^/        /'
		note ""
		note "The README is asserting something the module proxy does not agree with."
		note "Reality, from the proxy:"
		local known
		known=$(cd "$app" && $GO list -m -versions "$readme_path" 2>/dev/null | cut -d' ' -f2-)
		if [ -n "$known" ]; then
			note "  published versions of $readme_path: $known"
			note "  the README says @$readme_version"
		else
			note "  the proxy served no version list for $readme_path"
			note "  either the path is wrong, or nothing has been published yet"
		fi
		note ""
		note "Fix the README. Do not relax this gate: the line a consumer copies is"
		note "the one line in this repository whose only reader has no repository."
		return 1
	fi

	# 4. The extracted import, compiled byte for byte in the form the document
	#    published it. Two import declarations are legal Go, which is what lets a
	#    single-line import be used as written instead of reflowed into a group;
	#    a grouped spec is put back inside a group for the same reason. Rewriting
	#    one form into the other would mean the line under test is no longer the
	#    line the reader copies.
	if [ "$grouped" = 1 ]; then
		cat > "$app/main.go" <<-EOF
		package main

		import "fmt"

		import (
			$import_raw
		)

		func main() {
			fmt.Println("SDK_VERSION=" + $alias.Version())
		}
		EOF
	else
		cat > "$app/main.go" <<-EOF
		package main

		import "fmt"

		$import_raw

		func main() {
			fmt.Println("SDK_VERSION=" + $alias.Version())
		}
		EOF
	fi

	head2 "The documented import, compiled"

	for step in "mod tidy" "build ./..." "vet ./..."; do
		# shellcheck disable=SC2086
		if out=$(cd "$app" && $GO $step 2>&1); then
			pass "go $step"
		else
			fail "go $step failed against the documented import"
			note "$import_raw"
			printf '%s\n' "$out" | tail -8 | sed 's/^/        /'
			return 1
		fi
	done

	# 5. The alias is documentation OF the package name here — the README says
	#    so in as many words — so an alias that is not the package's real name
	#    teaches a selector that does not appear in any of the examples below it.
	local real_name
	real_name=$(cd "$app" && $GO list -f '{{.Name}}' "$import_path" 2>/dev/null)
	if [ "$real_name" = "$alias" ]; then
		pass "the documented alias is the published package's real name: $alias"
	else
		fail "$readme:$import_line — the alias is not the package's name"
		note "$import_raw"
		note "  documented alias : $alias"
		note "  published package: ${real_name:-<could not be determined>}"
		note "Go accepts any alias, so this compiles and still misleads: every"
		note "example in this README uses '$alias.' as its selector, and a reader"
		note "who copies the import without the alias gets '${real_name:-?}.' instead."
		return 1
	fi

	# Every symbol the go examples name, checked against the published version
	# rather than against the working tree. `go doc` answers for types, funcs,
	# consts and vars alike, which is what makes this possible at all.
	local missing=0 seen="" sym
	while IFS=$'\t' read -r _ pkg sym; do
		[ "$pkg" = "$alias" ] || continue
		case " $seen " in *" $sym "*) continue ;; esac
		seen="$seen $sym"
		if ! (cd "$app" && $GO doc "$import_path" "$sym" >/dev/null 2>&1); then
			[ "$missing" -eq 0 ] && fail "$readme names symbols that @$readme_version does not export"
			missing=$((missing + 1))
			note "  $alias.$sym is used in a go example and does not exist"
		fi
	done < <(printf '%s\n' "$PARSED" | grep '^symbol')
	if [ "$missing" -gt 0 ]; then
		note "The examples were written against a different version of the SDK than"
		note "the one the install section tells the reader to fetch."
		return 1
	fi
	pass "every symbol the go examples name exists in @$readme_version"

	# 6. The version the reader is told to fetch is the version they get.
	if [ "$readme_version" = latest ]; then
		pass "version cross-check skipped: the README documents @latest"
	elif out=$(cd "$app" && $GO run . 2>&1); then
		local reported
		reported=$(printf '%s' "$out" | grep '^SDK_VERSION=' | cut -d= -f2)
		if [ "$reported" = "$readme_version" ]; then
			pass "the module reports $reported, the version the README documents"
		else
			fail "the fetched module reports a different version than the README"
			note "  README says : @$readme_version"
			note "  module says : $reported"
			return 1
		fi
	else
		fail "the smoke program did not run"
		printf '%s\n' "$out" | tail -8 | sed 's/^/        /'
		return 1
	fi

	return 0
}

# ─── Self-test ──────────────────────────────────────────────────────────────
#
# A gate that has never failed is decoration. This drives the gate against
# deliberately broken copies of the real README — including the v0.4.2 bug, a
# version the proxy does not serve — and fails if any of them passes.
#
# The last case is the one people forget: the REAL README must still pass, or
# "every case failed" would be indistinguishable from "the script always fails".
self_test() {
	local fixtures real="$SDK_MODULE_DIR/README.md" bad=0
	fixtures=$(mktemp -d "$WORKROOT/selftest.XXXXXX")

	# expect_fail <name> <file> — the gate must reject this README.
	#
	# check_readme runs in a command substitution, so each case gets its own
	# subshell: the scrubbed GOPROXY/GOMODCACHE of one case cannot survive into
	# the next, and a case that passes when it should not cannot poison a later
	# one.
	expect_fail() {
		local name="$1" file="$2" log rc
		log=$(FAILURES=0 check_readme "$file" 2>&1)
		rc=$?
		if [ "$rc" -eq 0 ]; then
			printf '  \033[31mx\033[0m NOT DETECTED: %s\n' "$name"
			printf '%s\n' "$log" | sed 's/^/        /'
			bad=$((bad + 1))
		else
			printf '  \033[32m+\033[0m detected: %s\n' "$name"
			# The reason, so the self-test shows the gate failing for the right
			# cause and not by accident somewhere earlier.
			printf '%s\n' "$log" | sed 's/\x1b\[[0-9;]*m//g' | grep '^  x ' | head -1 | sed 's/^  x /        → /'
		fi
	}

	head2 "Self-test — the gate must reject each of these"

	# The historical bug, exactly: a version the proxy does not serve.
	sed 's|@v0\.1\.0|@v9.9.9|g' "$real" > "$fixtures/unpublished-version.md"
	expect_fail "a version that is not published (@v9.9.9)" "$fixtures/unpublished-version.md"

	# The tag written where the version belongs — the pre-Slice-16 mistake.
	sed 's|sdk/go@v0\.1\.0|sdk/go@sdk/go/v0.1.0|' "$real" > "$fixtures/tag-as-version.md"
	expect_fail "the git tag used as the version query" "$fixtures/tag-as-version.md"

	# A module path nobody publishes.
	sed 's|/sdk/go|/sdk/gone|g' "$real" > "$fixtures/wrong-module-path.md"
	expect_fail "a module path this repository does not publish" "$fixtures/wrong-module-path.md"

	# The install command and the import drifting apart.
	sed '/^go get /s|/sdk/go|/sdk/gone|' "$real" > "$fixtures/get-import-disagree.md"
	expect_fail "the install command and the import disagreeing" "$fixtures/get-import-disagree.md"

	# An alias that is not the package's name.
	sed 's|^import lightweight |import lw |' "$real" > "$fixtures/wrong-alias.md"
	expect_fail "an alias that is not the published package name" "$fixtures/wrong-alias.md"

	# A go example naming a method the published version does not have.
	sed 's|client\.Users\.List(ctx, lightweight\.UserListOptions|client.Users.List(ctx, lightweight.UserQuery|' \
		"$real" > "$fixtures/missing-symbol.md"
	expect_fail "a go example naming a symbol that does not exist" "$fixtures/missing-symbol.md"

	# The install command with nothing importing it afterwards.
	awk '!/^import lightweight /' "$real" > "$fixtures/no-import.md"
	expect_fail "an install command with no import beside it" "$fixtures/no-import.md"

	# The install block gone entirely. The heading is left alone on purpose: the
	# extractor no longer anchors on one, so removing the command is what has to
	# be detected, not renaming the section around it.
	awk '!/^go get /' "$real" > "$fixtures/no-install-command.md"
	expect_fail "the install command removed from the document" "$fixtures/no-install-command.md"

	# ── The grouped import form, which only the getting-started guide uses ──
	local grouped="docs/getting-started/CONNECT_BACKEND.md"
	if [ -f "$grouped" ]; then
		sed 's|^\tlightweight "|\tlw "|' "$grouped" > "$fixtures/grouped-wrong-alias.md"
		expect_fail "a wrong alias inside a grouped import" "$fixtures/grouped-wrong-alias.md"

		sed 's|@v0\.1\.0|@v9.9.9|g' "$grouped" > "$fixtures/grouped-unpublished.md"
		expect_fail "an unpublished version in the getting-started guide" "$fixtures/grouped-unpublished.md"
	else
		printf '  \033[33m!\033[0m skipped: %s is gone, so the grouped-import cases cannot run\n' "$grouped"
		bad=$((bad + 1))
	fi

	# ── The coverage guard, which is the gate on the gate's own list ──
	#
	# Proven by hiding a document from GATED_DOCS rather than by creating a file:
	# the guard reads tracked files through `git grep`, so an untracked fixture
	# would be invisible to it and the case would pass for the wrong reason.
	local saved="$GATED_DOCS"
	GATED_DOCS="$SDK_MODULE_DIR/README.md"
	if check_gate_coverage >/dev/null 2>&1; then
		printf '  \033[31mx\033[0m NOT DETECTED: a gated document dropped from the list\n'
		bad=$((bad + 1))
	else
		printf '  \033[32m+\033[0m detected: a document publishing the command with no gate reading it\n'
	fi
	GATED_DOCS="$saved"

	head2 "Self-test — and must accept the real ones"
	local log rc doc
	for doc in $GATED_DOCS; do
		log=$(FAILURES=0 check_readme "$doc" 2>&1)
		rc=$?
		if [ "$rc" -eq 0 ]; then
			printf '  \033[32m+\033[0m accepted: %s as committed\n' "$doc"
		else
			printf '  \033[31mx\033[0m FALSE POSITIVE: the real %s was rejected\n' "$doc"
			printf '%s\n' "$log" | sed 's/^/        /'
			bad=$((bad + 1))
		fi
	done

	head2 "Self-test verdict"
	if [ "$bad" -eq 0 ]; then
		printf '  \033[32m+\033[0m the gate rejects every broken quickstart and accepts the real one\n'
		return 0
	fi
	printf '  \033[31mx\033[0m %s self-test case(s) came out wrong — this gate is not trustworthy\n' "$bad"
	return 1
}

# ─── Which documents are gated, and the guard that keeps the list honest ────
#
# Three documents publish the install command as an INSTRUCTION — something a
# reader copies and runs. All three are gated:
GATED_DOCS="README.md $SDK_MODULE_DIR/README.md docs/getting-started/CONNECT_BACKEND.md"

# One document mentions the command without instructing anyone to run it, and is
# exempt for a reason that is written down rather than assumed:
#
#   docs/SDK_GO.md  explains the difference between the tag and the version, and
#                   quotes captured output of first-publish-smoke.sh. Its `go get`
#                   appears inside a discussion OF the command and inside a
#                   transcript, not as a step. Gating prose whose subject is the
#                   hazard teaches people to stop documenting the hazard.
EXEMPT_DOCS="docs/SDK_GO.md"

# The guard. A new document publishing `go get …` would otherwise be born
# outside this gate and stay there silently — which is how the SDK's own
# getting-started guide came to carry an ungated install command in the first
# place. Every tracked Markdown file naming the command must be in one list or
# the other, and adding a third list is not a fix.
check_gate_coverage() {
	head2 "Which documents publish the install command"

	local uncovered=0 f
	while IFS= read -r f; do
		case " $GATED_DOCS " in *" $f "*) continue ;; esac
		case " $EXEMPT_DOCS " in *" $f "*) continue ;; esac
		[ "$uncovered" -eq 0 ] && fail "a document publishes 'go get' for this module and no gate reads it"
		uncovered=$((uncovered + 1))
		note "  $f"
		# git grep rather than a find/xargs pipeline: it searches tracked files
		# only, so a self-test fixture in /tmp or a scratch note never widens the
		# list, and it cannot hang on an empty argument list.
	done < <(git grep -l "go get .*$ROOT_MODULE_PATH" -- '*.md' 2>/dev/null | sort)

	if [ "$uncovered" -gt 0 ]; then
		note ""
		note "Add it to GATED_DOCS in this script if a reader is meant to run the"
		note "command, or to EXEMPT_DOCS with the reason if the mention is prose"
		note "about the command. Silence is not one of the options: an install"
		note "instruction nobody executes is the exact shape of the v0.4.2 bug."
		return 1
	fi

	local d
	for d in $GATED_DOCS; do pass "gated: $d"; done
	for d in $EXEMPT_DOCS; do printf '  \033[33m!\033[0m exempt: %s (prose about the command, not an instruction)\n' "$d"; done
	return 0
}

# ─── Arguments ──────────────────────────────────────────────────────────────

EXTRACT_ONLY=0
DOCS="$GATED_DOCS"
SINGLE=0

case "${1:-}" in
	--self-test)
		self_test
		exit $?
		;;
	--extract)
		EXTRACT_ONLY=1
		if [ -n "${2:-}" ]; then DOCS="$2"; SINGLE=1; fi
		;;
	-h|--help)
		usage
		;;
	--*)
		echo "unknown option: $1" >&2
		usage
		;;
	"")
		;;
	*)
		DOCS="$1"
		SINGLE=1
		;;
esac

result=0

# The coverage guard runs only for the full set. Pointed at one file on purpose,
# the caller already knows what they are asking about.
if [ "$SINGLE" -eq 0 ] && [ "$EXTRACT_ONLY" -eq 0 ]; then
	check_gate_coverage || result=1
fi

failed_docs=""
for doc in $DOCS; do
	if ! check_readme "$doc"; then
		result=1
		failed_docs="$failed_docs $doc"
	fi
done

head2 "Verdict"
if [ "$result" -eq 0 ]; then
	if [ "$EXTRACT_ONLY" = 1 ]; then
		printf '  \033[32m+\033[0m parsed; no network check was run (--extract)\n'
	else
		printf '  \033[32m+\033[0m every documented quickstart works as written:%s\n' \
			"$(printf '%s' " $DOCS" | sed 's/ / /g')"
	fi
	exit 0
fi
if [ -n "$failed_docs" ]; then
	printf '  \033[31mx\033[0m does not work as written:%s\n' "$failed_docs"
else
	printf '  \033[31mx\033[0m the set of gated documents is out of date\n'
fi
exit 1
