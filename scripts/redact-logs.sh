#!/usr/bin/env bash
# redact-logs.sh — strip credential material from files before they are kept.
#
# CI uploads the e2e run's logs so a failure can be diagnosed. Those logs come
# from a run that minted real project credentials, created real Keycloak clients
# and printed key prefixes. A CI artifact outlives the job, is downloadable by
# anyone with read access to the repository, and is the sort of thing people
# attach to issues.
#
# The credentials are throwaway — the realms are deleted on exit and the
# database is a service container — so nothing here is a live secret. That is
# not the reason to redact. The reason is that a pipeline which publishes
# credential-shaped strings teaches everyone reading it that credential-shaped
# strings in logs are normal, and the day one of them is real, nobody notices.
#
# ─── What it matches ────────────────────────────────────────────────────────
#
# Shapes, not a list of known values. A list would have to be threaded from
# wherever the secret was generated to wherever the log is written, and would
# silently miss the one that took a different path.
#
#   lw_sk_<lookup>_<secret>   project credentials
#   eyJ…                      JWTs (three base64url segments)
#   password/secret/token=…   key=value assignments in any log line
#   Authorization: Bearer …   headers, whatever follows
#   postgres://user:pass@     connection strings
#
# Redaction is IN PLACE and irreversible, which is correct for something that
# runs immediately before an upload.
#
# Usage:
#   scripts/redact-logs.sh <directory-or-file> [...]
set -euo pipefail

if [ $# -eq 0 ]; then
  echo "usage: $0 <directory-or-file> [...]" >&2
  exit 2
fi

# Collect the target files.
files=()
for target in "$@"; do
  if [ -d "$target" ]; then
    while IFS= read -r f; do files+=("$f"); done < <(find "$target" -type f)
  elif [ -f "$target" ]; then
    files+=("$target")
  fi
done

if [ ${#files[@]} -eq 0 ]; then
  echo "  redact: nothing to do"
  exit 0
fi

redacted_total=0

for f in "${files[@]}"; do
  # Skip anything that is not text: a binary would be mangled and there is no
  # log format here that is not plain text.
  if ! LC_ALL=C grep -qI . "$f" 2>/dev/null; then
    continue
  fi

  before="$(wc -c < "$f")"

  python3 - "$f" <<'PY'
import re
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8", errors="replace") as fh:
    text = fh.read()

patterns = [
    # Project credentials. The prefix is fixed and greppable by design, which
    # is exactly what makes this reliable.
    (re.compile(r"lw_sk_[a-z2-7]{16}_[a-z2-7]{52}"), "lw_sk_REDACTED"),
    # A partial key prefix is still an identifier for a live credential.
    (re.compile(r"lw_sk_[A-Za-z0-9_]{4,}"), "lw_sk_REDACTED"),
    # JWTs: three base64url segments. Matches access tokens and id tokens.
    (re.compile(r"eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}"), "JWT_REDACTED"),
    # Bearer headers, whatever the scheme carries.
    (re.compile(r"(?i)(authorization\s*:\s*bearer\s+)\S+"), r"\1REDACTED"),
    # key=value and key: value assignments for secret-ish names.
    #
    # The name may be prefixed — KEYCLOAK_CLIENT_SECRET, LW_API_KEY — so the
    # match allows leading word characters instead of anchoring on a word
    # boundary, which underscores do not provide.
    (re.compile(r"(?i)([A-Za-z_]*(?:secret|password|passwd|api_?key|token|master_key))"
                r"([=:]\s*)(\"?)([^\s\"',)]+)"), r"\1\2\3REDACTED"),
    # Connection strings carry the database password in the userinfo.
    (re.compile(r"(postgres(?:ql)?://[^:@/\s]+:)[^@\s]+@"), r"\1REDACTED@"),
    # OAuth material in a URL query.
    #
    # internal/logging/access_log.go now redacts these before they are ever
    # written, so this pattern should find nothing in a log produced by a
    # current binary. It stays because logs outlive binaries: a diagnostics
    # bundle can contain a log from a container built before that fix, and the
    # upload step is the last place to catch it.
    (re.compile(r"(?i)([?&](?:code|state|session_state|code_verifier|id_token_hint|access_token|refresh_token|id_token)=)[^&\s\"']+"),
     r"\1REDACTED"),
]

count = 0
for pattern, replacement in patterns:
    text, n = pattern.subn(replacement, text)
    count += n

with open(path, "w", encoding="utf-8") as fh:
    fh.write(text)
PY

  n="$(python3 - "$f" <<'PY'
import sys
# Count what SURVIVED, as a self-check: any remaining match means a pattern
# above is wrong, and reporting zero would hide that.
import re
text = open(sys.argv[1], encoding="utf-8", errors="replace").read()
leaks = re.findall(r"lw_sk_[a-z2-7]{16}_[a-z2-7]{52}|eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}", text)
print(len(leaks))
PY
)"
  if [ "$n" != "0" ]; then
    echo "  redact: $f still contains $n credential-shaped value(s) after redaction" >&2
    exit 1
  fi

  after="$(wc -c < "$f")"
  if [ "$before" != "$after" ]; then
    redacted_total=$((redacted_total + 1))
  fi
done

echo "  + redacted ${redacted_total} of ${#files[@]} file(s)"
