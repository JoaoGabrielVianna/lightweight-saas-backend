#!/bin/sh
# Acquire a Keycloak access token via Direct Access Grants and call /me.
# Requires the docker-compose stack to be up.
set -eu

KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8081}"
REALM="${KEYCLOAK_REALM:-saas}"
CLIENT_ID="${KEYCLOAK_CLIENT_ID:-saas-backend}"
CLIENT_SECRET="${KEYCLOAK_CLIENT_SECRET:-saas-backend-secret}"
USERNAME="${USERNAME:-testuser}"
PASSWORD="${PASSWORD:-password}"
API_URL="${API_URL:-http://localhost:8080}"

echo "Requesting token from $KEYCLOAK_URL/realms/$REALM as $USERNAME..."
TOKEN=$(curl -fsS -X POST "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=$CLIENT_ID" \
  -d "client_secret=$CLIENT_SECRET" \
  -d "grant_type=password" \
  -d "username=$USERNAME" \
  -d "password=$PASSWORD" | jq -r '.access_token')

if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
  echo "ERROR: failed to obtain access token"
  exit 1
fi
echo "+ token acquired (length: ${#TOKEN})"

echo "Calling GET $API_URL/me with bearer token..."
curl -fsS -i "$API_URL/me" -H "Authorization: Bearer $TOKEN"
echo
