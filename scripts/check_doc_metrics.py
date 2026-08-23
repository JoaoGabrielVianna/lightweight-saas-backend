#!/usr/bin/env python3
"""check_doc_metrics.py — fail if a number in the docs disagrees with the code.

Why this exists
---------------
Before 2026-07-26 the README advertised "22 routes" against an actual 46, and
"13 canonical actions" against 14. Nobody lied; the code moved and the prose
did not. That class of rot (recorded as TD-001) is entirely mechanical, so it
gets a machine.

Each entry below pairs a value DERIVED FROM CODE with the documents that
publish it. If they diverge, this fails and names both sides.

Adding a metric
---------------
Append to METRICS. `derive` must be a shell one-liner printing a single integer
on stdout. `claims` lists (file, regex) pairs where the regex has exactly one
capture group holding the number as published.

If a metric becomes genuinely hard to derive, delete it rather than letting it
rot — a check nobody trusts is worse than no check.

Usage:  python3 scripts/check_doc_metrics.py [--quiet]
Exit:   0 = docs match code · 1 = at least one divergence
"""

import re
import subprocess
import sys

# (label, shell derivation, [(doc path, regex with one capture group), ...])
METRICS = [
    (
        "Go packages",
        "go list ./... | wc -l",
        [("docs/PROJECT_STATUS.md", r"\|\s*Go packages\s*\|\s*(\d+)\s*\|"),
         ("docs/ARCHITECTURE.md", r"one deployable binary, (\d+) packages")],
    ),
    (
        "Go source files (non-test)",
        r"find . -name '*.go' -not -name '*_test.go' -not -path './.git/*' | wc -l",
        [("docs/PROJECT_STATUS.md", r"\|\s*Go source files \(non-test\)\s*\|\s*(\d+)\s*\|")],
    ),
    (
        "Go test files",
        r"find . -name '*_test.go' -not -path './.git/*' | wc -l",
        [("docs/PROJECT_STATUS.md", r"\|\s*Go test files\s*\|\s*(\d+)\s*\|")],
    ),
    (
        "Go test functions",
        r"grep -rh '^func Test' --include='*_test.go' . | wc -l",
        [("docs/PROJECT_STATUS.md", r"\|\s*Go test functions\s*\|\s*(\d+)\s*\|")],
    ),
    (
        # The PRODUCT surface. Added 2026-08-16, when the published route total
        # was found to be 46 — the count from before workspaces existed, which
        # omitted all 47 of these. The README's central claim about what
        # LIGHTWEIGHT is now rests on this number, so it is derived.
        "Product API routes (/v1)",
        r"grep -cE 'v1\.(GET|POST|PUT|PATCH|DELETE)\(' internal/server/router.go",
        [("docs/PROJECT_STATUS.md", r"\|\s*— \*\*Product API \(`/v1/\*`\)\*\*\s*\|\s*\*\*(\d+)\*\*\s*\|"),
         ("README.md", r"(\d+) routes under `/v1`")],
    ),
    (
        # Every route the server registers, across every group and every
        # conditional mount. Counted from source rather than from a built
        # router so this needs no Go toolchain.
        "HTTP routes (total)",
        r"ls internal/server/*.go | grep -v _test.go "
        r"| xargs grep -hoE '\.(GET|POST|PUT|PATCH|DELETE|HEAD)\(\"' | wc -l",
        [("docs/PROJECT_STATUS.md", r"\|\s*\*\*HTTP routes \(total\)\*\*\s*\|\s*\*\*(\d+)\*\*\s*\|")],
    ),
    (
        "Admin API routes",
        r"grep -cE 'admin\.(GET|POST|PUT|PATCH|DELETE)\(' internal/server/router.go",
        [("docs/PROJECT_STATUS.md", r"\|\s*— Admin API \(`/admin/\*`, fully gated\)\s*\|\s*(\d+)\s*\|"),
         ("README.md", r"\|\s*(\d+) gated routes:"),
         ("docs/MODULES.md", r"\|\s*`/admin/\*` API\s*\|\s*(\d+)\s*\|")],
    ),
    (
        # The credential vocabulary. Published in the README's concept table and
        # in the getting-started guide, because "what may this key do" is the
        # first question a reader asks about a project credential.
        # Derived from AllScopes(), which is the RUNTIME authority rather than
        # the const block above it: IsKnownScope iterates it, NormalizeScopes
        # validates a new credential against it, and GET /v1/project-scopes
        # serves it. A constant declared but left out of AllScopes is not part
        # of the vocabulary, and the documented number must follow the
        # vocabulary.
        #
        # NO `\t` IN THE PATTERN. This metric shipped as
        # `grep -cE '^\tScope...'` and passed on macOS and busybox, whose greps
        # read `\t` as a tab, while GNU grep -- the CI runner's -- reads it as a
        # literal `t`, matched nothing, derived 0, and failed the build against
        # documentation that was correct. POSIX ERE has no `\t`, so match the
        # identifiers themselves and let `sort -u` do the counting.
        "Credential scopes",
        r"""sed -n '/^func AllScopes() \[\]Scope {/,/^}/p' internal/authz/scope.go """
        r"""| grep -oE 'Scope[A-Z][A-Za-z]*' | sort -u | wc -l""",
        [("docs/PROJECT_STATUS.md", r"\|\s*Credential scopes\s*\|\s*(\d+)\s*\|"),
         ("README.md", r"What a credential may do\. \*\*(\d+)\*\* exist"),
         ("docs/getting-started/FIRST_CREDENTIAL.md", r"There are \*\*(\d+)\*\*, and this is all of them")],
    ),
    (
        # The configuration surface. The onboarding docs lead with "41 declared,
        # you supply three", and that first number is the one a reader uses to
        # decide whether to be afraid of .env.example. contract_test.go already
        # pins .env.example against the contract table, so this only has to
        # catch the PROSE drifting away from both.
        "Declared environment variables",
        r"grep -cE '^[A-Z][A-Z0-9_]*=' .env.example",
        [("docs/operations/RUNNING.md", r"\*\*(\d+) variables are declared"),
         ("docs/getting-started/KEYCLOAK_EXISTING.md", r"read all (\d+) lines of `\.env\.example`")],
    ),
    (
        "Canonical audit actions",
        r"grep -cE '^\s+Action[A-Za-z]+ +Action = ' internal/audit/event.go",
        [("docs/PROJECT_STATUS.md", r"\|\s*Canonical audit actions \(declared = emitted\)\s*\|\s*(\d+)\s*\|"),
         ("README.md", r"it records\. (\d+) canonical actions")],
    ),
    (
        "IdentityProvider methods",
        r"sed -n '/^type IdentityProvider interface/,/^}/p' internal/identity/provider.go "
        r"| grep -cE '^\s+[A-Z][A-Za-z]+\(ctx'",
        [("docs/PROJECT_STATUS.md", r"\|\s*`IdentityProvider` interface methods\s*\|\s*(\d+)\s*\|"),
         ("docs/ARCHITECTURE.md", r"(\d+) methods across users, roles, sessions")],
    ),
    (
        "HTTP handler methods",
        r"grep -rhcE '^func \(h \*[A-Za-z]+\) [A-Za-z]+\(c \*gin\.Context\)' "
        r"internal/identity/handler.go internal/user/handler.go internal/server/smtp_handler.go "
        r"internal/server/email_templates_handler.go internal/server/audit_handler.go "
        r"| awk '{s+=$1} END {print s}'",
        [("docs/PROJECT_STATUS.md", r"\|\s*HTTP handler methods\s*\|\s*(\d+)\s*\|")],
    ),
    (
        "Admin console SPA views",
        "ls web/admin/static/js/views/*.js | wc -l",
        [("README.md", r"SPA at `/admin`\. (\d+) views:"),
         ("docs/MODULES.md", r"(\d+) `views/` ·")],
    ),
    (
        # *.test.mjs, not *.mjs: tests/ also holds helpers.mjs, which node's
        # runner never executes as a suite. Counting it would publish a number
        # one higher than the number of suites that actually run.
        "Frontend test suites",
        "ls web/admin/static/js/tests/*.test.mjs | wc -l",
        [("docs/MODULES.md", r"across (\d+) `node --test` suites")],
    ),
    (
        "Docker Compose services",
        r"grep -cE '^  [a-z][a-z-]*:$' docker-compose.yml",
        [("docs/PROJECT_STATUS.md", r"\|\s*Docker Compose services\s*\|\s*(\d+)\s*\|")],
    ),
    (
        # Added 2026-08-24. ARCHITECTURE.md said "three tables" from the moment
        # workspaces shipped until v0.4.2 found it — two releases during which
        # the canonical architecture document understated the schema by half,
        # while PROJECT_STATUS.md carried the right number a few files away.
        #
        # Neither existing gate could see it. check_docs_links.py reads links,
        # not claims; check_metrics only compares numbers it has been told to
        # derive, and nobody had told it about this one. The number is a single
        # grep over the migrations, and it is published in two documents, which
        # is exactly the shape that rots.
        #
        # Counting CREATE TABLE in the up migrations, rather than querying a
        # live database, keeps this runnable in the no-services CI job. Every
        # migration is idempotent (IF NOT EXISTS), so each table is created
        # exactly once across the set.
        "Database tables owned",
        r"grep -rhcE 'CREATE TABLE (IF NOT EXISTS )?[a-z_]+' "
        r"internal/database/migrations/*.up.sql | paste -sd+ - | bc",
        [("docs/PROJECT_STATUS.md", r"\|\s*Database tables owned\s*\|\s*(\d+)\s*\|"),
         ("docs/ARCHITECTURE.md", r"The service owns \*\*(\d+) tables\*\*")],
    ),
]


def derive(cmd: str) -> int:
    out = subprocess.run(
        cmd, shell=True, capture_output=True, text=True, check=False
    ).stdout.strip()
    # Some derivations (grep -c over several files) print one count per line.
    nums = [int(t) for t in re.findall(r"\d+", out)]
    if not nums:
        raise SystemExit(f"derivation produced no number: {cmd!r} -> {out!r}")
    return sum(nums) if len(nums) > 1 else nums[0]


def main() -> int:
    quiet = "--quiet" in sys.argv
    failures, checked = [], 0

    rows = []
    for label, cmd, claims in METRICS:
        actual = derive(cmd)
        for path, pattern in claims:
            try:
                text = open(path, encoding="utf-8").read()
            except FileNotFoundError:
                failures.append((label, path, "—", actual, "document not found"))
                continue

            m = re.search(pattern, text)
            if not m:
                failures.append(
                    (label, path, "—", actual,
                     "claim not found — did the doc's wording change?")
                )
                continue

            checked += 1
            claimed = int(m.group(1))
            rows.append((label, path, claimed, actual, claimed == actual))
            if claimed != actual:
                failures.append((label, path, claimed, actual, "documented value is stale"))

    if not quiet:
        print(f"  {'metric':<32} {'document':<26} {'doc':>5} {'code':>5}")
        for label, path, claimed, actual, ok in rows:
            mark = "ok" if ok else "**"
            print(f"  {label:<32} {path:<26} {claimed:>5} {actual:>5}  {mark}")
        print()

    if failures:
        print(f"✗ {len(failures)} documentation/code divergence(s):\n")
        for label, path, claimed, actual, why in failures:
            print(f"  {label}")
            print(f"      {path}: documented {claimed}, code says {actual}  ({why})")
        print("\nRe-derive the number, do not copy it between documents.")
        print("See docs/PROJECT_STATUS.md#metrics for the derivation commands.")
        return 1

    if not quiet:
        print(f"  + doc metrics OK ({checked} published claims match the code)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
