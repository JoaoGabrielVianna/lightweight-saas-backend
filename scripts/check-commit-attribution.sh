#!/usr/bin/env bash
# check-commit-attribution.sh — reject AI attribution in commit messages.
#
# Why this exists
# ---------------
# This repository's policy is that a commit records who is accountable for the
# change, and that is a person. Tooling used to produce it -- an editor, a
# formatter, a code-generating agent -- is not a co-author and does not sign
# the commit.
#
# The policy was real but existed only in conversation, so nothing enforced it.
# Three commits reached published history carrying a `Co-Authored-By` trailer
# naming an AI model, because the agent's own default is to append one and no
# machine ever disagreed. Those commits are ancestors of the published v0.4.0
# and sdk/go/v0.1.0 tags and cannot be rewritten; see docs/QUALITY_GATE.md.
# This script is the machine that disagrees, so it cannot happen again.
#
# What it rejects
# ---------------
#   Co-Authored-By: <anything naming a known AI agent or vendor>
#   Generated with / by Claude · Claude Code · AI · ChatGPT · …  (footer form)
#   AI-assisted · AI-generated                                   (as compounds)
#   claude.ai · chatgpt.com · claude.com/claude-code             (attribution URLs)
#   assisted/written/authored/created by <an AI>
#
# What it deliberately does NOT reject
# ------------------------------------
#   Co-Authored-By: Jane Developer <jane@example.com>   — human co-authors are
#     the entire point of the trailer and stay welcome.
#   The letters "ai" inside ordinary words (email, chain, fail, maintainer),
#     or a technical commit that happens to discuss AI. Only the hyphenated
#     compounds and the footer forms match.
#
# Modes
# -----
#   --file  <path>        one commit-message file      (the commit-msg hook)
#   --commit <rev>        one existing commit
#   --range <A..B>        every commit in a range       (CI)
#   --all                 every reachable commit        (audit; see NOTE below)
#   (no arguments)        defaults to origin/main..HEAD
#
# NOTE on --all: it reports the three published commits named above. That is
# correct, not a bug -- they are immutable and the report should stay honest.
# Never wire --all into a gate; use --range so a gate judges only new commits.
#
# Exit:  0 = clean · 1 = attribution found · 2 = usage error
set -uo pipefail

usage() {
  sed -n '/^# Modes/,/^#   (no arguments)/p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

# ── Patterns ───────────────────────────────────────────────────────────────
# One list, used by the hook and by CI, so the two can never drift apart.
# All matching is case-insensitive (grep -iE).

# Vendors and agents that identify a non-human author. Deliberately a closed
# list of names: matching the bare word "bot" or "ai" would flag `dependabot`
# and `email`, and a gate with false positives is a gate people bypass.
AI_NAMES='claude|anthropic|chatgpt|openai|copilot|codex|gemini|devin|cursor|windsurf|llm|language model'

# Two parallel arrays rather than one "regex:reason" list. The obvious
# delimiter-joined form is a trap here: `${entry%%:*}` would cut the very first
# pattern at the colon inside `co-authored-by:`, leaving an invalid regex that
# grep rejects and `|| true` swallows — a gate that reports clean on every
# input. That is exactly the failure this file exists to prevent, so the data
# structure does not permit it.
PATTERNS=()
REASONS=()

rule() { PATTERNS+=("$1"); REASONS+=("$2"); }

# 1. A Co-Authored-By trailer naming one of them, or using a vendor no-reply
#    address. Human trailers do not match.
rule "^[[:space:]]*co-authored-by:.*(${AI_NAMES}|noreply@anthropic\.com|@openai\.com)" \
     "Co-Authored-By trailer naming an AI agent"

# 2. The "Generated with …" footer, in any of its emoji/bullet decorated forms.
#    Anchored near the start of a line so that prose using the words in the
#    middle of a sentence does not trip it.
rule "^.{0,8}generated (with|by) .{0,40}(${AI_NAMES}|\bai\b)" \
     '"Generated with/by" attribution footer'

# 3. The hyphenated compounds. These are only ever attribution.
rule '\bai-(assisted|generated|authored|written)\b' \
     "AI-assisted / AI-generated marker"

# 4. Attribution URLs.
rule '(claude\.ai|claude\.com/claude-code|chat\.openai\.com|chatgpt\.com|copilot\.github\.com)' \
     "link to an AI attribution URL"

# 5. Prose attribution.
rule "\b(assisted|written|authored|created|produced) by (an? )?(${AI_NAMES})\b" \
     "prose crediting an AI as the author"

# ── Core ───────────────────────────────────────────────────────────────────
# scan <label> <message-text>  → 0 clean, 1 attribution found
scan() {
  local label="$1" text="$2" found=0 i regex reason hits rc

  for i in "${!PATTERNS[@]}"; do
    regex="${PATTERNS[$i]}"
    reason="${REASONS[$i]}"

    # grep exits 0 on match, 1 on no match, ≥2 on a bad regex. A rule that
    # cannot compile must be a loud failure, never a silent pass — see the
    # note above the rule table for the bug this catches.
    printf '' | grep -qE "$regex" 2>/dev/null
    rc=$?
    if [ "$rc" -gt 1 ]; then
      echo "  ✗ internal error: attribution rule $i does not compile: $regex" >&2
      exit 2
    fi

    hits=$(printf '%s\n' "$text" | grep -inE "$regex" || true)
    [ -z "$hits" ] && continue

    if [ "$found" -eq 0 ]; then
      printf '\n  ✗ AI attribution in %s\n' "$label"
      found=1
    fi
    printf '      %s:\n' "$reason"
    printf '%s\n' "$hits" | sed 's/^/        /'
  done

  return "$found"
}

# strip_comments — remove what git itself would remove before storing.
#
# The commit-msg hook sees the raw file: `#` comment lines are still present,
# and under `git commit -v` the whole staged diff is appended after a scissors
# line. Without this, committing a change to THIS FILE would flag itself.
strip_comments() {
  sed '/^#[[:space:]]*-\{2,\}[[:space:]]*>8[[:space:]]*-\{2,\}/,$d' | grep -v '^#' || true
}

fail_count=0

check_file() {
  local path="$1" msg
  [ -f "$path" ] || { echo "  ✗ no such message file: $path" >&2; exit 2; }
  msg=$(strip_comments < "$path")
  scan "the commit message" "$msg" || fail_count=$((fail_count + 1))
}

check_commits() {
  local revs="$1" sha subject msg
  [ -z "$revs" ] && return 0
  while read -r sha; do
    [ -z "$sha" ] && continue
    subject=$(git log -1 --format='%s' "$sha")
    msg=$(git log -1 --format='%B' "$sha")
    scan "$(git rev-parse --short "$sha") — ${subject}" "$msg" || fail_count=$((fail_count + 1))
  done <<< "$revs"
}

# ── Self-test ──────────────────────────────────────────────────────────────
# The rules are five regexes, and a regex that silently matches nothing looks
# exactly like a clean repository. The first draft of this file had that bug:
# the pattern list was built as "regex:reason" strings, and `${entry%%:*}` cut
# the Co-Authored-By rule at the colon inside `co-authored-by:`. Every one of
# the seven trailer cases below passed a gate that was doing nothing.
#
# So the gate is tested, not read. Cases marked `bad` must be rejected, `good`
# must be accepted, and both directions matter equally: a gate with false
# positives gets bypassed, and a bypassed gate catches nothing.
SELF_TEST_TMP=""
cleanup_self_test() { [ -n "$SELF_TEST_TMP" ] && rm -rf "$SELF_TEST_TMP"; return 0; }

self_test() {
  local tmp pass=0 failed=0 rejected=0 accepted=0 verdict expect label body rc

  tmp=$(mktemp -d) || exit 2
  SELF_TEST_TMP="$tmp"
  trap cleanup_self_test EXIT

  _case() {
    expect="$1" label="$2" body="$3"
    if [ "$expect" -eq 1 ]; then rejected=$((rejected + 1)); else accepted=$((accepted + 1)); fi
    printf '%s\n' "$body" > "$tmp/msg"
    # check_file only increments fail_count; it is the caller that turns that
    # into an exit status, so the subshell has to do the same. Returning the
    # status of the last assignment instead would report every case as clean.
    ( fail_count=0; check_file "$tmp/msg"; exit "$fail_count" ) >/dev/null 2>&1
    rc=$?
    if [ "$rc" -eq "$expect" ]; then
      verdict="ok"; pass=$((pass + 1))
    else
      verdict="FAILED (rc=$rc, want $expect)"; failed=$((failed + 1))
      printf '      ✗ %-52s %s\n' "$label" "$verdict"
    fi
  }

  # ── must be rejected ─────────────────────────────────────────────────────
  _case 1 'Co-Authored-By: Claude'            'feat: x

Co-Authored-By: Claude <noreply@anthropic.com>'
  _case 1 'Co-Authored-By: Claude Opus 5'     'feat: x

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>'
  _case 1 'lowercase trailer'                 'feat: x

co-authored-by: claude code <x@y.z>'
  _case 1 'uppercase trailer, Anthropic'      'feat: x

CO-AUTHORED-BY: ANTHROPIC <x@y.z>'
  _case 1 'mixed case, ChatGPT'               'feat: x

Co-AuThOrEd-By: ChAtGpT <x@openai.com>'
  _case 1 'OpenAI Codex'                      'feat: x

Co-Authored-By: OpenAI Codex <codex@openai.com>'
  _case 1 'GitHub Copilot'                    'feat: x

Co-Authored-By: GitHub Copilot <copilot@github.com>'
  _case 1 'neutral name, vendor no-reply'     'feat: x

Co-Authored-By: Assistant <noreply@anthropic.com>'
  _case 1 'indented trailer'                  'feat: x

   Co-Authored-By: Claude <x@y.z>'
  _case 1 'Generated with, emoji and link'    'feat: x

🤖 Generated with [Claude Code](https://claude.com/claude-code)'
  _case 1 'Generated by Claude'               'feat: x

Generated by Claude'
  _case 1 'GENERATED BY AI'                   'feat: x

GENERATED BY AI'
  _case 1 'AI-assisted'                       'feat: x

AI-assisted change'
  _case 1 'ai-generated'                      'feat: x

this file is ai-generated'
  _case 1 'claude.ai link'                    'feat: x

See https://claude.ai/chat/123'
  _case 1 'prose: written by Claude'          'feat: x

This patch was written by Claude.'
  _case 1 'prose: created by an LLM'          'feat: x

Created by an LLM.'

  # ── must be accepted ─────────────────────────────────────────────────────
  _case 0 'plain conventional commit'         'feat: add workspace resolver'
  _case 0 'human co-author'                   'feat: x

Co-Authored-By: Jane Developer <jane@example.com>'
  _case 0 'two human co-authors'              'feat: x

Co-Authored-By: Jane Developer <jane@example.com>
Co-Authored-By: Bob Maintainer <bob@example.org>'
  _case 0 'dependabot'                        'chore(deps): bump x

Co-Authored-By: dependabot[bot] <support@github.com>'
  _case 0 'human named Aisha Nair'            'feat: x

Co-Authored-By: Aisha Nair <aisha@example.com>'
  _case 0 '"ai" inside ordinary words'        'fix(email): retry the mail chain for a failed maintainer domain'
  _case 0 '"generated by" mid-sentence'       'feat(secrets): the keyring is generated by scripts/init.sh at install time'
  _case 0 'regenerated swagger'               'docs(swagger): regenerate for PUT /admin/users/:id/password'
  _case 0 'commented-out trailer'             'feat: x

# Co-Authored-By: Claude <noreply@anthropic.com>'
  _case 0 'git commit -v diff of this file'   'feat: x

# ------------------------ >8 ------------------------
diff --git a/scripts/check-commit-attribution.sh
+Co-Authored-By: Claude <noreply@anthropic.com>
+AI-generated'

  if [ "$failed" -ne 0 ]; then
    printf '  ✗ attribution gate self-test: %s passed, %s FAILED\n' "$pass" "$failed" >&2
    exit 1
  fi
  printf '  + attribution gate self-test: %s cases (%s rejected, %s accepted)\n' \
    "$pass" "$rejected" "$accepted"
}

MODE="" ARG=""
case "${1:-}" in
  --file)   MODE=file;   ARG="${2:-}"; [ -z "$ARG" ] && usage ;;
  --commit) MODE=commit; ARG="${2:-}"; [ -z "$ARG" ] && usage ;;
  --range)  MODE=range;  ARG="${2:-}"; [ -z "$ARG" ] && usage ;;
  --all)    MODE=all ;;
  --self-test) self_test; exit 0 ;;
  -h|--help) usage ;;
  "")       MODE=default ;;
  *)        usage ;;
esac

case "$MODE" in
  file)
    check_file "$ARG"
    ;;
  commit)
    git rev-parse --verify --quiet "${ARG}^{commit}" >/dev/null || {
      echo "  ✗ not a commit: $ARG" >&2; exit 2; }
    check_commits "$(git rev-parse "${ARG}^{commit}")"
    ;;
  range)
    check_commits "$(git rev-list "$ARG" 2>/dev/null || true)"
    ;;
  all)
    check_commits "$(git rev-list --all)"
    ;;
  default)
    if ! git rev-parse --verify --quiet origin/main >/dev/null; then
      echo "  ✗ origin/main is not available (shallow clone?)." >&2
      echo "    Pass an explicit range: $0 --range <base>..HEAD" >&2
      exit 2
    fi
    check_commits "$(git rev-list origin/main..HEAD 2>/dev/null || true)"
    ;;
esac

if [ "$fail_count" -ne 0 ]; then
  cat >&2 <<'EOF'

  A commit records who is accountable for the change. Tools used to write it
  are not co-authors. Remove the attribution and commit again:

      git commit --amend          (for the commit you just made)

  Human co-authors are welcome and unaffected:

      Co-Authored-By: Jane Developer <jane@example.com>

  Policy: CLAUDE.md · CONTRIBUTING.md
EOF
  exit 1
fi

exit 0
