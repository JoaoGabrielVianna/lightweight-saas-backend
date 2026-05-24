# Dev Auth Console

Browser-based developer console for the Keycloak integration. Served at **`http://localhost:8080/dev/auth`** when `DEV_PLAYGROUND_ENABLED=true`.

Single page · six cards. The backend's `/auth/debug` endpoint is the single source of truth for validation — the UI consumes it and translates failures into human "why + fix" blocks, but never re-decides validity client-side.

> **Developer observability tool, not product UI.** Not hardened, accessible, internationalized, or branded. Gated behind the env flag plus a Keycloak client that exists only when `features.dev_playground=true` in [`config/project.json`](../../config/project.json).

---

## Console layout

| #  | Card                  | Source-of-truth                          | What you do here |
|----|-----------------------|------------------------------------------|------------------|
| 1  | **connection**        | `/health` + KC realm root + OIDC discovery | See whether each piece of the auth stack is reachable. Auto-refreshes every 10s. |
| 2  | **authentication**    | local PKCE flow + `/auth/debug` for user info | Login / refresh / logout. Live expiry countdown. Copy token to clipboard. |
| 3  | **token introspection**| `/auth/debug` (entire response)         | Every claim and config check the API uses, with a green/yellow/red pill on `valid`. |
| 4  | **debugging**         | `/auth/debug.reason`                     | Only visible when `valid=false`. Human "why + fix" translation. |
| 5  | **api testing**       | live calls with the active bearer        | `GET /me`, `GET /health`. Placeholder buttons for future endpoints. |
| 6  | **raw payloads**      | `/auth/debug`, `/me`, local JWT decode   | Collapsible JSON dumps with copy buttons. Client-side JWT decode is cosmetic only. |

The validation discipline is strict: every `valid`, `expired`, `roles`, `email`, `aud`, `iss` value comes from `/auth/debug`. Replacing the cosmetic JWT decode would not change any validation outcome.

### Token-side specifics

- **Access token masked by default** (first 12 + last 8 chars). Copy uses `navigator.clipboard.writeText` — never displays the full token.
- **Decoded claims shown:** `sub`, `email`, `preferred_username`, `roles` (flattened from `realm_access.roles`), `issuer`, `azp`, `expires` (ISO + relative).
- **Diagnostics colour-coded** green/red/grey for `/health`, OIDC discovery, `/dev/auth/config.json`. Re-runnable.

---

## Architecture

```
Browser → API serves /dev/auth (static HTML + /dev/auth/config.json)
       → Login button → window.location.assign() → KC /auth?client_id=saas-dev-playground&pkce=S256
       ← KC login page → callback ?code=&state=
       → POST /token (PKCE verifier) → KC returns access_token + refresh_token
       → GET /me Bearer ... → API validates JWT via JWKS → 200 { id, sub, email, ... }
```

The browser performs Authorization Code + PKCE against a **second** Keycloak client (`saas-dev-playground`) that exists only for this playground:

- `publicClient: true` — no client secret
- `pkce.code.challenge.method: S256` — KC enforces PKCE
- `directAccessGrantsEnabled: false` — no password grant from browser
- `redirectUris: ["http://localhost:8080/dev/auth"]` — only the playground URL

The API's main client (`saas-backend`) is untouched and stays confidential.

### `azp` whitelist — why both clients work

Tokens carry an `azp` (authorized party) claim equal to the minting client. The API enforces:

```
KEYCLOAK_ALLOWED_CLIENT_IDS=saas-backend,saas-dev-playground
```

`bootstrap` writes this line into `.env` automatically when `features.dev_playground=true`. Tokens minted by either client pass; anything else fails with `azp ... is not in the allowed-client set` (see [getting-started/KEYCLOAK_SETUP.md §2](../getting-started/KEYCLOAK_SETUP.md#2-environment-variables)). With the feature flag `false`, the generator omits the playground client id, so production deployments accept only the primary client.

---

## Enabling it

```bash
# 1. Flag in config/project.json:  "features": { "dev_playground": true, ... }
make regen        # regen .env + deploy/keycloak/realm-export.json
make realm-reset  # nuke KC DB; re-import on startup
docker-compose up -d --no-deps api
```

Open <http://localhost:8080/dev/auth>. When `false` (default), all `/dev/auth*` routes 404.

---

## File layout

```
web/dev/
├── auth.html        shell + ids the JS hooks into
├── auth.js          PKCE flow, token inspection, /me, diagnostics
└── styles.css       plain CSS, no framework

internal/server/playground.go   conditional route mount; reads ./web/dev from disk
internal/bootstrap/generate.go  writeRealmExport adds saas-dev-playground when flag on
```

Static assets are read from disk (`./web/dev/`) at request time — Go's `//go:embed` can't reach paths outside the embedding package directory. The Dockerfile `COPY web /app/web` ensures files are available in the container.

---

## Security constraints

| Constraint | Mechanism |
|------------|-----------|
| Never enabled in production | `DEV_PLAYGROUND_ENABLED` defaults to `false`; bootstrap only writes `true` when the feature flag is on. |
| No client secret in the browser | Public Keycloak client. PKCE replaces the secret as proof-of-possession. |
| No hardcoded passwords | Login redirects to Keycloak's own login page; playground never sees the password. |
| Tokens never persisted beyond the session | `sessionStorage` only (cleared on tab close). No `localStorage`, no cookies. |
| Full access token never rendered | Masked display by default. Copy reads from `sessionStorage` to clipboard. |
| Single-use authorization codes | URL is `history.replaceState`'d after PKCE exchange — refreshing the page doesn't re-spend the code. |
| CSRF protection on auth redirect | `state` param generated client-side, stored in `sessionStorage`, verified on callback. |
| Robots ignore the page | `<meta name="robots" content="noindex,nofollow">`. |

---

## `/auth/debug` — token introspection endpoint

Companion to the playground UI for the curl-and-jq crowd. Same gate (`DEV_PLAYGROUND_ENABLED=true`), different URL namespace. Collapses "why is my token rejected?" into one call.

### Request

```
GET /auth/debug                                    # config introspection
GET /auth/debug   Authorization: Bearer <jwt>      # token introspection (header wins)
GET /auth/debug?token=<jwt>                        # token via query
```

### Response — always HTTP 200

**Observational, not a gate.** The `valid` field carries the answer; HTTP is always 200 so a curl pipeline doesn't error out on rejection.

```json
{
  "issuer":          "http://localhost:8081/realms/saas",
  "allowed_clients": ["saas-backend", "saas-dev-playground"],
  "received_azp":    "saas-dev-playground",
  "received_sub":    "fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc",
  "exp":             "2026-05-18T17:45:59Z",
  "expired":         false,
  "iat":             "2026-05-18T16:45:59Z",
  "aud":             ["account"],
  "email":           "testuser@test.com",
  "roles":           ["user", "offline_access"],
  "valid":           true,
  "reason":          ""
}
```

| Field             | Source | When claim absent |
|-------------------|--------|--------------------|
| `issuer`          | API config — `KEYCLOAK_URL + /realms/ + KEYCLOAK_REALM` | always present |
| `allowed_clients` | API config — `KEYCLOAK_ALLOWED_CLIENT_IDS` (with `{KEYCLOAK_CLIENT_ID}` fallback) | always present |
| `received_azp`    | Token `azp` decoded locally **without signature verification** — shown even on tamper | `""` |
| `received_sub`    | Token `sub`, same caveat | `""` |
| `exp` / `iat`     | Token `exp` / `iat`, RFC3339 UTC | `""` |
| `expired`         | `time.Now().After(exp)` — strict, no clock-skew leeway (matches validator) | `false` |
| `aud`             | Token `aud`, normalized to string array (RFC 7519 §4.1.3) | `[]` |
| `email`           | Token `email` | `""` |
| `roles`           | Deduped union of `realm_access.roles` + `resource_access.<KEYCLOAK_CLIENT_ID>.roles` | `[]` |
| `valid`           | `true` iff `provider.ValidateToken` accepted (signature + iss + azp + exp + sub all pass) | always present |
| `reason`          | Empty on `valid=true`. Otherwise provider error verbatim — e.g. `azp "X" is not in the allowed-client set` or `token is expired` | always present |

**Empty values instead of omitted fields** — response shape stays stable across token scope profiles, so `jq -e '.expired == true'` works without `// false` fallbacks. **Not surfaced:** custom mapper outputs, nonces, ACR values, KC session bookkeeping (`sid`, `session_state`). Base64-decode the JWT segment yourself if needed. Nothing the server holds (client secret, JWKS, DB rows) is ever included.

### Canonical examples

**No token (config introspection):**
```bash
$ curl -sS http://localhost:8080/auth/debug | jq '{issuer, allowed_clients, valid, reason}'
{
  "issuer":          "http://localhost:8081/realms/saas",
  "allowed_clients": ["saas-backend", "saas-dev-playground"],
  "valid":           false,
  "reason":          "no token supplied (use Authorization: Bearer <jwt> or ?token=<jwt>)"
}
```

**Rejected token (unknown azp):**
```json
{
  "received_azp": "unlisted-client",
  "valid":        false,
  "reason":       "invalid token: azp \"unlisted-client\" is not in the allowed-client set"
}
```

The `reason` mirrors exactly what the API's audit log shows for the same rejection — no log-diving required.

**Expired token:**
```json
{
  "exp":     "2026-05-18T16:46:05Z",
  "expired": true,
  "valid":   false,
  "reason":  "token expired: token has invalid claims: token is expired"
}
```

Client-side `expired` check and validator's verdict agree byte-for-byte — both use strict `time.Now().After(exp)`.

**Missing role (RBAC debug):**
```bash
$ curl -sS http://localhost:8080/auth/debug -H "Authorization: Bearer $T" | jq '.roles | contains(["admin"])'
false
```

Empty `roles` for a user you expected to have them → probably forgotten the `roles` client scope (KC admin UI → Clients → saas-backend → Client scopes → Add `roles` to defaults).

### From the playground browser console

```js
fetch("/auth/debug", {
  headers: { Authorization: "Bearer " + sessionStorage.kc_dev_access_token }
}).then(r => r.json()).then(console.table);
```

### Constraints

- **Mounted only when `DEV_PLAYGROUND_ENABLED=true`.** Default `false`. Production deployments don't set it; route 404s.
- **Surfaces unverified claim values** (`received_azp`, `received_sub`) so you can debug tampered/forged tokens. Safe — everything in the response is data the caller already has (their own token + the API's public expected values). No new secrets exposed.
- **No auth bypass.** Endpoint never grants access — only reports. Protected endpoints still require a valid bearer.

---

## Limitations

- **Local dev only.** Designed for `http://localhost:8080/dev/auth`. Reverse proxy requires updating Keycloak client's `redirectUris` and `webOrigins`.
- **No user-list management.** Use Keycloak's admin UI at `http://localhost:8081` to add/remove users.
- **No interactive request builder.** Single `GET /me` button today. Extending to arbitrary endpoints: see `callMe` in [auth.js](../../web/dev/auth.js).
- **The page itself isn't authenticated.** Anyone reaching the URL can attempt to log in. Fine because the URL only resolves locally.
- **Hand-rolled OIDC.** Robust enough for a dev tool. For production frontends, swap to [keycloak-js](https://www.keycloak.org/securing-apps/javascript-adapter) — the public-touchpoint surface (element ids, `config.json` shape) stays the same.

---

## Troubleshooting

| Symptom | Cause / Fix |
|---------|-------------|
| `/dev/auth` returns 404 | `DEV_PLAYGROUND_ENABLED` not `true` in API env. Set in `.env`, regenerate, restart api. |
| Login redirects to KC, returns `error=invalid_redirect_uri` | `saas-dev-playground` client isn't registered (need `make realm-reset` after `make regen`), OR browser hits playground on a port not matching `redirectUris`. |
| Token exchange fails with `unauthorized_client` | Same root cause as above. |
| Diagnostics: red dot on Keycloak | KC container isn't running, or `KEYCLOAK_URL` in `/dev/auth/config.json` points to the wrong host. Must be browser-reachable (i.e. `http://localhost:8081`, not `http://keycloak:8080`). |
| `/me` returns 401 with `key not found: kid` | JWKS cache stale (typically after `make realm-reset` regenerated KC's signing keys). Cache refreshes on kid-miss within ~30s. If persists, `docker-compose restart api`. |
| Logout doesn't clear KC session | Browser preserved KC SSO cookie. Open KC admin → Sessions → revoke. Or use a private tab. |

For non-playground troubleshooting, see [getting-started/KEYCLOAK_SETUP.md §9](../getting-started/KEYCLOAK_SETUP.md#9-troubleshooting).

---

## Disabling

```bash
# config/project.json:  "features": { "dev_playground": false, ... }
make regen && docker-compose up -d --no-deps api
# or at runtime:
DEV_PLAYGROUND_ENABLED=false docker-compose up -d --no-deps api
```

The `saas-dev-playground` client remains in the realm export until the feature flag is flipped back — harmless (KC keeps a registered-but-unused public client). To remove entirely, regenerate and re-import.
