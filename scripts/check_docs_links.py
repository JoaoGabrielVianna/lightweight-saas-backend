#!/usr/bin/env python3
"""check_docs_links.py — fail if any Markdown link points at something absent.

Why this exists
---------------
The 2026-07-26 documentation audit found 36 broken links across the repository,
including four in CONTRIBUTING.md pointing at files that never existed
(CODE_OF_CONDUCT.md, two issue templates, a PR template). Dead links are the
cheapest possible signal that documentation has drifted from reality, and they
are trivially machine-checkable — so they should never be found by a human
again.

What it checks
--------------
1. Relative file links resolve to something on disk.
2. Anchor fragments (#section) exist as a heading in the target document,
   using GitHub's slug algorithm.

What it deliberately skips
--------------------------
- http/https/mailto/file: URIs (no network calls: this must run offline, fast,
  and deterministically in CI).
- Links inside inline code spans, which are documentation ABOUT link syntax
  rather than actual links (docs/archive/DOCS_REORG_REPORT.md has these).
- docs/evidence/**, which is a frozen archive of raw artifacts from manual runs.

Usage:  python3 scripts/check_docs_links.py [--quiet]
Exit:   0 = all links resolve · 1 = at least one broken
"""

import os
import re
import sys

SKIP_DIRS = {".git", "node_modules", "bin", "dist"}
SKIP_PATH_PARTS = ("docs/evidence/",)
SKIP_SCHEMES = ("http://", "https://", "mailto:", "file:", "tel:")

LINK_RE = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*)")
CODE_SPAN_RE = re.compile(r"`[^`]*`")
FENCE_RE = re.compile(r"^\s*```")


def slug(heading: str) -> str:
    """Reproduce GitHub's heading-anchor algorithm.

    Strips inline markdown, lowercases, drops punctuation, then maps EACH
    whitespace character to one hyphen. The last part matters: "V1-01 · Foo"
    loses the "·" and keeps both surrounding spaces, yielding a DOUBLE hyphen
    ("v1-01--foo"). Collapsing whitespace here would produce false positives on
    every heading in ROADMAP.md.
    """
    s = heading.strip()
    s = re.sub(r"`([^`]*)`", r"\1", s)
    s = re.sub(r"\*\*([^*]*)\*\*", r"\1", s)
    s = re.sub(r"\*([^*]*)\*", r"\1", s)
    s = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", s)
    s = s.lower()
    return "".join(
        "-" if c.isspace() else c
        for c in s
        if c.isspace() or c in "-_" or c.isalnum()
    )


def markdown_files(root: str):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            if not name.endswith(".md"):
                continue
            path = os.path.join(dirpath, name)
            rel = os.path.relpath(path, root).replace(os.sep, "/")
            if any(part in rel for part in SKIP_PATH_PARTS):
                continue
            yield path, rel


def collect_anchors(path: str) -> set:
    anchors, in_fence = set(), False
    with open(path, encoding="utf-8", errors="ignore") as fh:
        for line in fh:
            if FENCE_RE.match(line):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            m = HEADING_RE.match(line)
            if m:
                anchors.add(slug(m.group(2)))
    return anchors


def main() -> int:
    quiet = "--quiet" in sys.argv
    root = os.getcwd()

    files = list(markdown_files(root))
    anchors = {path: collect_anchors(path) for path, _ in files}

    problems = []
    for path, rel in files:
        base = os.path.dirname(path)
        in_fence = False
        with open(path, encoding="utf-8", errors="ignore") as fh:
            for lineno, line in enumerate(fh, 1):
                if FENCE_RE.match(line):
                    in_fence = not in_fence
                    continue
                if in_fence:
                    continue
                # Links written inside `backticks` document syntax, not targets.
                for target in LINK_RE.findall(CODE_SPAN_RE.sub("", line)):
                    if target.startswith(SKIP_SCHEMES):
                        continue
                    filepart, _, frag = target.partition("#")

                    if not filepart:  # same-document anchor
                        if frag and slug(frag) not in anchors[path]:
                            problems.append((rel, lineno, target, "anchor not found in this file"))
                        continue

                    resolved = os.path.normpath(os.path.join(base, filepart))
                    if not os.path.exists(resolved):
                        problems.append((rel, lineno, target, "path does not exist"))
                    elif frag and resolved in anchors and slug(frag) not in anchors[resolved]:
                        problems.append((rel, lineno, target, "anchor not found in target"))

    if problems:
        print(f"✗ {len(problems)} broken link(s):\n")
        for rel, lineno, target, why in problems:
            print(f"  {rel}:{lineno}")
            print(f"      -> {target}   ({why})")
        print("\nFix the link, or the target, before merging.")
        return 1

    if not quiet:
        print(f"  + docs links OK ({len(files)} markdown files, 0 broken)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
