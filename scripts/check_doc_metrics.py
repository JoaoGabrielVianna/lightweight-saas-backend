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
        "Admin API routes",
        r"grep -cE 'admin\.(GET|POST|PUT|PATCH|DELETE)\(' internal/server/router.go",
        [("docs/PROJECT_STATUS.md", r"\|\s*— Admin API \(`/admin/\*`, fully gated\)\s*\|\s*(\d+)\s*\|"),
         ("README.md", r"\|\s*(\d+) gated routes:"),
         ("docs/MODULES.md", r"\|\s*`/admin/\*` API\s*\|\s*(\d+)\s*\|")],
    ),
    (
        "Canonical audit actions",
        r"grep -cE '^\s+Action[A-Za-z]+ +Action = ' internal/audit/event.go",
        [("docs/PROJECT_STATUS.md", r"\|\s*Canonical audit actions \(declared = emitted\)\s*\|\s*(\d+)\s*\|"),
         ("README.md", r"structured event\. (\d+) canonical actions")],
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
        [("README.md", r"`/admin`\. (\d+) views, PKCE login"),
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
