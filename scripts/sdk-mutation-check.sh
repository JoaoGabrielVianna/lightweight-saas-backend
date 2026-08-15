#!/usr/bin/env bash
# sdk-mutation-check.sh — prove the SDK's test suite actually fails when the
# behaviour it claims to pin is broken.
#
# A green suite proves the code passes the tests. It does not prove the tests
# would notice if the code were wrong, and the two are routinely confused. A test
# that asserts on a value nobody sets, or that swallows the error it should be
# reading, is green forever and worth nothing — and it looks exactly like a good
# one in a diff.
#
# So this script BREAKS the SDK on purpose, one property at a time, and requires
# the suite to go red each time. A mutation that survives is reported as a hole
# in the tests, not as a passing check.
#
# ─── How it works, and why it is safe ───────────────────────────────────────
#
# The SDK source is copied into a scratch directory and mutated THERE. The
# working tree is never modified, so an interrupted run cannot leave a sabotaged
# client behind — which is the failure mode that makes "temporarily edit a file"
# tooling dangerous.
#
#   ./scripts/sdk-mutation-check.sh
#   ./scripts/sdk-mutation-check.sh -v     # show the failing test output
set -uo pipefail

VERBOSE=0
for arg in "$@"; do
  case "$arg" in
    -v|--verbose) VERBOSE=1 ;;
    *) echo "unknown flag: $arg"; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SDK_SRC="$REPO_ROOT/sdk/go"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/lw-sdk-mutation.XXXXXX")"
trap 'rm -rf "$WORKDIR"' EXIT

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mCAUGHT\033[0m  %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mSURVIVED\033[0m %s\n' "$1"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# mutate <description> <file> <python-replacement-expression>
#
# The replacement is a small Python program run against the file's text, because
# sed's portability differences between GNU and BSD are exactly the kind of thing
# that would make this script silently apply no mutation and report a hole that
# is not there.
mutate() { # description file python-snippet [test-filter]
  local description="$1" file="$2" snippet="$3" filter="${4:-}"

  rm -rf "$WORKDIR/sdk"
  cp -R "$SDK_SRC" "$WORKDIR/sdk"
  rm -f "$WORKDIR/sdk/coverage.out"

  if ! python3 - "$WORKDIR/sdk/$file" <<PY
import sys
path = sys.argv[1]
src = open(path).read()
before = src
$snippet
if src == before:
    sys.stderr.write("the mutation matched nothing\n")
    sys.exit(3)
open(path, "w").write(src)
PY
  then
    bad "$description — the mutation did not apply, so nothing was tested"
    return
  fi

  local out rc
  if [ -n "$filter" ]; then
    out="$(cd "$WORKDIR/sdk" && go test -count=1 -run "$filter" ./... 2>&1)"
  else
    out="$(cd "$WORKDIR/sdk" && go test -count=1 ./... 2>&1)"
  fi
  rc=$?

  if [ "$rc" -ne 0 ]; then
    ok "$description"
    [ "$VERBOSE" = "1" ] && printf '%s\n' "$out" | grep -E '^\s+---|FAIL|\.go:' | head -8 | sed 's/^/      /'
  else
    bad "$description"
    echo "      No test failed. The property is unprotected: a regression here would ship."
  fi
}

step "sanity: the unmutated SDK is green"
rm -rf "$WORKDIR/sdk"
cp -R "$SDK_SRC" "$WORKDIR/sdk"
rm -f "$WORKDIR/sdk/coverage.out"
if ! (cd "$WORKDIR/sdk" && go test -count=1 ./... >/dev/null 2>&1); then
  echo "  ✗ the SDK suite is already failing; fix that before reading anything below"
  exit 1
fi
echo "  + baseline green"

step "mutations"

# 1. Authorization header omitted.
#    The most consequential possible regression: every call fails with a 401 that
#    looks like a bad credential, and an operator rotates keys that were fine.
mutate "the Authorization header is dropped" transport.go \
  'src = src.replace('"'"'req.Header.Set("Authorization", "Bearer "+c.apiKey)'"'"', "_ = c.apiKey")'

# 2. Wrong workspace in the path.
#    Silent cross-tenant addressing. The server refuses it — that is the point of
#    the binding — but a client that builds the wrong URL is a client whose
#    callers see workspace_mismatch for correct configuration.
mutate "the workspace is dropped from the path" transport.go \
  'src = src.replace("b.WriteString(c.workspace)", "b.WriteString(\"ws_00000000-0000-0000-0000-000000000000\")")'

# 3. Path segments no longer escaped.
mutate "caller-supplied path segments are no longer escaped" transport.go \
  'src = src.replace("b.WriteString(url.PathEscape(s))", "b.WriteString(s)")'

# 4. Typed errors collapsed into strings.
#    The single change that would do the most damage to consumers: every
#    errors.As in every backend stops matching, silently, and error handling
#    degrades to string comparison.
mutate "API errors are returned as plain strings" transport.go \
  'src = src.replace("return c.apiError(op, method, path, resp)", "return errors.New(\"request failed with status \" + http.StatusText(resp.StatusCode))")'

# 5. Request id dropped.
#    Nothing breaks. Every failure simply becomes uncorrelatable with the server
#    log — which is why it needs a test rather than a reviewer.
mutate "the request id is dropped from errors" transport.go \
  'src = src.replace("RequestID:  resp.Header.Get(requestIDHeader),", "RequestID:  \"\",")
src = src.replace("""	if env.Error.RequestID != "" {
		// The envelope'"'"'s copy wins over the header'"'"'s. They agree in practice;
		// preferring the body means a proxy that strips or rewrites the header
		// cannot cost a caller the correlation id.
		apiErr.RequestID = env.Error.RequestID
	}
""", "")'

# 6. A refusal treated as success.
#    This is the revoked-credential scenario in its most dangerous form: the 401
#    is ignored, the caller gets a zero value, and a backend concludes the
#    workspace is empty.
mutate "non-2xx responses are treated as success" transport.go \
  'src = src.replace("if resp.StatusCode < 200 || resp.StatusCode > 299 {", "if false {")'

# 7. Forward compatibility removed.
#    Turning this on looks like a tightening. It is a promise to break every
#    deployed consumer the next time the server adds an optional field.
mutate "unknown response fields are rejected" transport.go \
  'src = src.replace("""	if err := json.Unmarshal(raw, out); err != nil {""", """	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {""")'

# 8. The API key leaks into an error.
#    The exact shape of a well-meaning debugging change.
mutate "the API key is interpolated into a transport error" transport.go \
  'src = src.replace("return &RequestError{Op: op, Method: method, Path: path, Err: unwrapURLError(err)}", "return &RequestError{Op: op, Method: method, Path: path, Err: fmt.Errorf(\"%w (key %s)\", unwrapURLError(err), c.apiKey)}")'

# 9. The API key leaks through formatting.
#    Removing the redaction restores Go'"'"'s default struct formatting, which prints
#    every field including the credential.
mutate "Config stops redacting the API key" client.go \
  'src = src.replace("c.BaseURL, c.WorkspaceID, redactKey(c.APIKey), presence(c.HTTPClient != nil), c.UserAgent)", "c.BaseURL, c.WorkspaceID, c.APIKey, presence(c.HTTPClient != nil), c.UserAgent)")'

# 10. The temporary password leaks through formatting.
mutate "CreateUserRequest stops redacting the temporary password" users.go \
  'src = src.replace("\", TemporaryPassword:<redacted>\" +", "\", TemporaryPassword:\" + r.TemporaryPassword +")'

# 11. Silent retries.
#    A client that retries a mutation it believes idempotent eventually creates
#    two users, and this API has no idempotency key to make that safe.
mutate "failed requests are silently retried" transport.go \
  'src = src.replace("""	resp, err := c.http.Do(req)""", """	resp, err := c.http.Do(req)
	if err == nil && resp.StatusCode >= 500 {
		_ = resp.Body.Close()
		req2, _ := http.NewRequestWithContext(ctx, method, endpoint, nil)
		req2.Header = req.Header.Clone()
		resp, err = c.http.Do(req2)
	}""")'

# 12. Unbounded error body.
mutate "the error body is read without a bound" transport.go \
  'src = src.replace("raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))", "raw, err := io.ReadAll(resp.Body)")'

# 12b. Unbounded success body.
mutate "the success body is read without a bound" transport.go \
  'src = src.replace("limited := io.LimitReader(resp.Body, maxResponseBodyBytes+1)", "limited := io.Reader(resp.Body)")
src = src.replace("""	if len(raw) > maxResponseBodyBytes {""", """	if false {""")'

# 13. Malformed success silently zero-valued.
mutate "a malformed success body decodes to a zero value" transport.go \
  'src = src.replace("""	if err := json.Unmarshal(raw, out); err != nil {""", """	if err := json.Unmarshal(raw, out); false {""")'

# 14. Empty path arguments accepted.
#     An empty id does not fail cleanly — it addresses a different endpoint.
mutate "empty path arguments are accepted" errors.go \
  'src = src.replace("""	if strings.TrimSpace(value) == "" {""", """	if false {""")'

# 15. The capability manifest quietly emptied.
#     The gate that stops a new project-accessible route going unnoticed has to
#     itself be checkable.
mutate "the SDK coverage manifest loses an entry" apicoverage.json \
  'import json
doc = json.loads(src)
doc["routes"] = [r for r in doc["routes"] if r.get("sdk") != "UsersService.Delete"]
src = json.dumps(doc, indent=2)' \
  'TestCoverage'

# ─── Summary ────────────────────────────────────────────────────────────────
step "mutation summary"
printf '  \033[32m%d caught\033[0m, \033[31m%d survived\033[0m\n' "$PASS" "$FAIL"
if [ "$FAIL" -ne 0 ]; then
  echo
  echo "  A surviving mutation means the SDK can be broken in that way without any"
  echo "  test noticing. Add the missing assertion; do not weaken the mutation."
  exit 1
fi
exit 0
