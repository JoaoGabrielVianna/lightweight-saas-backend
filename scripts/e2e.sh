#!/bin/sh
# End-to-end smoke test: waits for stack readiness then runs auth-test.
set -eu

API_URL="${API_URL:-http://localhost:8080}"
KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8081}"
MAX_WAIT=60

wait_for_url() {
  url="$1"; label="$2"
  i=0
  while [ "$i" -lt "$MAX_WAIT" ]; do
    if curl -fsS -o /dev/null "$url" 2>/dev/null; then
      echo "+ $label ready"
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "- timed out waiting for $label at $url"
  return 1
}

wait_for_url "$KEYCLOAK_URL/realms/master/.well-known/openid-configuration" "keycloak"
wait_for_url "$API_URL/health" "api"

exec "$(dirname "$0")/auth-test.sh"
