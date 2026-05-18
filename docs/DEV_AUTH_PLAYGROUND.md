# Dev Auth Console

A unified, browser-based developer console for the Keycloak integration.
Served at **`http://localhost:8080/dev/auth`** when the feature is enabled.

Single page · six cards · dark terminal aesthetic. The backend's
`/auth/debug` endpoint is the single source of truth for validation — the
UI consumes it and translates failures into human "why + fix" blocks, but
never re-decides validity client-side.

> **This is a developer observability tool, not product UI.** It is not
> designed to be hardened, accessible, internationalized, or styled to fit
> a brand. It exists to compress the auth debugging loop from
> "terminal → curl → jq → log dive" into a single page. It is gated behind
> an environment flag (`DEV_PLAYGROUND_ENABLED`) and a Keycloak client
> that is registered only when `features.dev_playground=true` in
> [`config/project.json`](../config/project.json).

---

## Console layout

Six stacked cards, top to bottom:

| #  | Card                  | Source-of-truth                          | What you do here                                                       |
|----|-----------------------|------------------------------------------|------------------------------------------------------------------------|
| 1  | **connection**        | `/health` + Keycloak realm root + OIDC discovery | See whether each piece of the auth stack is reachable. Auto-refreshes every 10s. |
| 2  | **authentication**    | local PKCE flow + `/auth/debug` for user info | Login / refresh / logout. Live expiry countdown. Copy token to clipboard. |
| 3  | **token introspection**| `/auth/debug` (entire response)         | Every claim and config check the API uses, with a green/yellow/red pill on `valid`. |
| 4  | **debugging**         | `/auth/debug.reason`                     | Only visible when `valid=false`. Human "why + fix" translation of the provider's error. |
| 5  | **api testing**       | live calls with the active bearer        | `GET /me`, `GET /health`. Placeholder buttons for future endpoints.   |
| 6  | **raw payloads**      | `/auth/debug`, `/me`, local JWT decode   | Collapsible JSON dumps with copy buttons. The local JWT decode here is cosmetic only — not used for any judgment. |

The validation discipline is strict: every `valid`, `expired`, `roles`,
`email`, `aud`, `iss` value the UI renders comes from `/auth/debug`. The
client-side JWT decode in card 6 is for human inspection only — replacing
it with a different display would not change any validation outcome.

## Purpose

What it replaces:

| Before (manual)                                                | After (this page)                  |
|---------------------------------------------------------------|------------------------------------|
| `curl -X POST .../token` with username/password in shell      | Click **Login with Keycloak**     |
| Pipe the JSON through `jq -r .access_token`                   | Auto-extracted + masked            |
| `pbcopy < token.txt` then paste into Swagger's `Authorize` box | Click **Copy** button              |
| `curl /me -H "Authorization: Bearer ..."`                     | Click **GET /me**                  |
| Inspect claims with `cut -d. -f2 | base64 -d`                 | Pre-rendered (sub/email/roles/exp) |
| Repeat after token expires                                    | Click **Refresh token**            |

What it validates:

- The Keycloak realm import worked (login redirects to Keycloak's login page).
- The PKCE client (`saas-dev-playground`) is configured correctly.
- The API's JWKS-based token validation accepts the issued token.
- `EnsureUser` JIT-provisions a local row from the token.
- The `/me` endpoint returns the same `id` across calls (stable identity).

---

## Architecture

```
┌────────────┐          ┌──────────────────────┐         ┌──────────────────┐
│  Browser   │  1. GET  │  API (Gin)           │         │   Keycloak       │
│  /dev/auth │─────────►│  serves static HTML  │         │   realm=saas     │
│            │          │  + /dev/auth/        │         │                  │
│            │          │    config.json       │         │                  │
│            │          └──────────────────────┘         │                  │
│            │          2. click "Login"                 │                  │
│            │              window.location.assign(...)  │                  │
│            │  ──────────────────────────────────────►  │ /auth?           │
│            │                                           │   client_id=     │
│            │                                           │     saas-dev-... │
│            │                                           │   pkce: S256     │
│            │                                           │                  │
│            │          3. Keycloak login page           │                  │
│            │          4. ?code=...&state=...           │                  │
│            │  ◄──────────────────────────────────────  │                  │
│            │  5. POST /token (PKCE verifier)           │                  │
│            │  ──────────────────────────────────────►  │ access_token     │
│            │  ◄──────────────────────────────────────  │ + refresh_token  │
│            │                                           │                  │
│            │  6. GET /me  Bearer ...                   │                  │
│            │  ───────►  API validates JWT via JWKS     │                  │
│            │            (fetches keys from Keycloak)   │                  │
│            │  ◄──────  200 { id, sub, email, ... }     │                  │
└────────────┘          └──────────────────────┘         └──────────────────┘
```

The browser performs Authorization Code + PKCE against a **second** Keycloak
client (`saas-dev-playground`) that exists only for this playground:

- `publicClient: true` — no client secret
- `pkce.code.challenge.method: S256` — Keycloak enforces PKCE
- `directAccessGrantsEnabled: false` — no password grant from the browser
- `redirectUris: ["http://localhost:8080/dev/auth"]` — only the playground URL

The API's main client (`saas-backend`) is untouched and stays confidential.

### azp whitelist — why both clients work

Tokens issued by Keycloak carry an `azp` (authorized party) claim equal to
the client that minted them. The API enforces a whitelist:

```
KEYCLOAK_ALLOWED_CLIENT_IDS=saas-backend,saas-dev-playground
```

`bootstrap` writes this line into `.env` automatically when
`features.dev_playground=true` (primary client first, playground client
second). Tokens minted by `saas-dev-playground` (when you use this page)
and tokens minted by `saas-backend` (curl examples, server-to-server) both
pass — anything else is rejected with `azp ... is not in the allowed-client
set` (see [KEYCLOAK_SETUP.md §2](./KEYCLOAK_SETUP.md#2-environment-variables)).

When `features.dev_playground=false`, the generator omits the playground
client id from the list, so production deployments only accept the primary
client by default.

---

## Enabling it

1. Flip the feature flag in [`config/project.json`](../config/project.json):

   ```json
   "features": { "dev_playground": true, ... }
   ```

2. Regenerate `.env` + `deploy/keycloak/realm-export.json`:

   ```bash
   make regen
   ```

3. Re-import the realm so Keycloak picks up the new client:

   ```bash
   make realm-reset      # nukes Keycloak DB; re-imports on startup
   ```

4. Restart (or recreate) the API container so it sees the new env vars:

   ```bash
   docker-compose up -d --no-deps api
   ```

5. Open <http://localhost:8080/dev/auth> in a browser.

When the flag is `false` (default), all `/dev/auth*` routes return 404.

---

## File layout

```
web/dev/
├── auth.html         shell + ids the JS hooks into
├── auth.js           PKCE flow, token inspection, /me, diagnostics
└── styles.css        plain CSS, no framework

internal/server/
└── playground.go     conditional route mount; reads ./web/dev from disk

internal/bootstrap/
└── generate.go       writeRealmExport adds saas-dev-playground client
                      when features.dev_playground=true
```

Static assets are read from disk (`./web/dev/`) at request time — Go's
`//go:embed` can't reach paths outside the embedding package directory, and
shuffling the files into the Go package tree would have made the layout
counter-intuitive. The Dockerfile `COPY web /app/web` ensures the files
are available in the container.

---

## What the page shows

### Header
- API base URL
- Realm name (live from `/dev/auth/config.json`)
- Client ID (`saas-dev-playground`)
- Redirect URI

A prominent yellow **DEV ONLY** badge.

### Diagnostics
Three coloured dots:
- **green**: reachable
- **red**: error (HTTP code or fetch failure shown inline)
- **grey**: still checking

Checks: `/health` on the API, OIDC discovery on Keycloak, the
`/dev/auth/config.json` endpoint itself. Re-runnable via a button.

### Authentication
Single status indicator (`authenticated` / `unauthenticated`) plus
context-sensitive buttons: **Login with Keycloak** before login, **Refresh
token** + **Logout** after.

### Token inspection
- Access token is shown **masked** by default (first 12 + last 8 chars).
- **Copy** button uses `navigator.clipboard.writeText` — never displays
  the full token on screen.
- Decoded claims: `sub`, `email`, `preferred_username`, `roles` (flattened
  from `realm_access.roles`), `issuer`, `azp`, `expires` (ISO + relative).

### API testing
**GET /me** button calls the protected endpoint with the bearer token and
displays HTTP status, latency, and pretty-printed JSON response.

---

## Security constraints (and why each one matters)

| Constraint                                              | Mechanism                                                                                  |
|---------------------------------------------------------|--------------------------------------------------------------------------------------------|
| Never enabled in production                             | `DEV_PLAYGROUND_ENABLED` defaults to `false`; bootstrap only writes `true` when the feature flag is on. |
| No client secret in the browser                         | Uses a public Keycloak client. PKCE replaces the secret as the proof-of-possession.        |
| No hardcoded passwords                                  | Login redirects to Keycloak's own login page; the playground never sees the password.      |
| Tokens never persisted beyond the session               | `sessionStorage` only (cleared on tab close). No `localStorage`, no cookies.              |
| Full access token never rendered                        | Masked display by default. Copy button reads from `sessionStorage` to clipboard.           |
| Single-use authorization codes                          | URL is `history.replaceState`'d after the PKCE exchange — refreshing the page doesn't re-spend the code. |
| CSRF protection on the auth redirect                    | `state` parameter generated client-side, stored in `sessionStorage`, verified on callback. |
| Robots / search engines ignore the page                 | `<meta name="robots" content="noindex,nofollow">` in the HTML.                             |

---

## `/auth/debug` — token introspection endpoint

Companion to the playground UI for the curl-and-jq crowd. Same gate
(`DEV_PLAYGROUND_ENABLED=true`), different URL namespace.

### Purpose

Collapse the typical "why is my token rejected?" debugging loop:

```
old loop                                          new loop
─────────────────────────────────────────         ───────────────────────────
1. curl /me                  → 401                curl /auth/debug
2. tail docker logs saas-api → reason             ── done
3. cut -d. -f2 | base64 -d   → claims
4. cross-reference with KEYCLOAK_URL config
5. cross-reference with KEYCLOAK_ALLOWED_CLIENT_IDS
```

### Request

```
GET /auth/debug
```

Optional token sources (use either; header wins when both present):

```
Authorization: Bearer <jwt>
```

or

```
GET /auth/debug?token=<jwt>
```

### Response — always HTTP 200

The endpoint is **observational, not a gate**. The `valid` field carries
the answer; the HTTP status is always 200 so a curl pipeline doesn't error
out on rejection.

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

| Field             | Source                                                                                                   | When claim absent  |
|-------------------|----------------------------------------------------------------------------------------------------------|--------------------|
| `issuer`          | API config — derived from `KEYCLOAK_URL + /realms/ + KEYCLOAK_REALM`.                                    | always present     |
| `allowed_clients` | API config — the `azp` whitelist (`KEYCLOAK_ALLOWED_CLIENT_IDS`, with `{KEYCLOAK_CLIENT_ID}` fallback).  | always present     |
| `received_azp`    | Token `azp` claim, decoded locally **without signature verification** — shown even on tamper.            | `""`               |
| `received_sub`    | Token `sub` claim, same caveat as `received_azp`.                                                        | `""`               |
| `exp`             | Token `exp` claim, rendered as RFC3339 UTC.                                                              | `""`               |
| `expired`         | `time.Now().After(exp)` — strict, no clock-skew leeway (matches the validator's behavior).               | `false`            |
| `iat`             | Token `iat` claim, RFC3339 UTC.                                                                          | `""`               |
| `aud`             | Token `aud` claim, always normalized to a string array (per RFC 7519 §4.1.3 — claim can be string OR array). | `[]`            |
| `email`           | Token `email` claim.                                                                                     | `""`               |
| `roles`           | Deduped union of `realm_access.roles` + `resource_access.<KEYCLOAK_CLIENT_ID>.roles`. Mirrors the keycloak provider's `identity.Roles`. | `[]` |
| `valid`           | `true` iff `provider.ValidateToken` accepted the token (signature + iss + azp + exp + sub all pass).     | always present     |
| `reason`          | Empty when `valid=true`. Otherwise the provider's error verbatim — e.g. `azp "X" is not in the allowed-client set` or `token is expired`. | always present |

**Why empty values instead of omitting fields?** The response shape stays
stable across tokens with different scope profiles. A consumer can write
`jq -e '.expired == true'` without `// false` fallbacks, regardless of
whether the supplied token carried an `exp` claim.

**What is NOT surfaced.** Only claims a developer needs to debug auth
failures (`iss`/`azp`/`sub`/`aud`/`exp`/`iat`/`email`/`roles`) appear in
the response. Custom mapper outputs, nonces, ACR values, and Keycloak's
internal session bookkeeping (`sid`, `session_state`) are intentionally
not exposed — base64-decode the second JWT segment yourself if you need
them. Nothing the server holds (client secret, JWKS, DB rows) is ever
included.

### Example: no token supplied (config introspection)

```bash
$ curl -sS http://localhost:8080/auth/debug | jq .
{
  "issuer":          "http://localhost:8081/realms/saas",
  "allowed_clients": ["saas-backend", "saas-dev-playground"],
  "received_azp":    "",
  "received_sub":    "",
  "valid":           false,
  "reason":          "no token supplied (use Authorization: Bearer <jwt> or ?token=<jwt>)"
}
```

Useful for answering "what does this API expect, exactly?" without any
token at all.

### Example: valid token from `saas-backend`

```bash
TOKEN=$(curl -fsS -X POST http://localhost:8081/realms/saas/protocol/openid-connect/token \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d 'client_id=saas-backend' -d 'client_secret=saas-backend-secret' \
  -d 'grant_type=password' -d 'username=testuser' -d 'password=password' \
  | jq -r .access_token)

curl -sS http://localhost:8080/auth/debug -H "Authorization: Bearer $TOKEN" | jq .
```

```json
{
  "issuer":          "http://localhost:8081/realms/saas",
  "allowed_clients": ["saas-backend", "saas-dev-playground"],
  "received_azp":    "saas-backend",
  "received_sub":    "fbe56e3a-...-37c655f3fbdc",
  "valid":           true,
  "reason":          ""
}
```

### Example: rejected token (unknown azp)

```json
{
  "received_azp": "unlisted-client",
  "valid":        false,
  "reason":       "invalid token: azp \"unlisted-client\" is not in the allowed-client set"
}
```

The `reason` mirrors exactly what the API's audit log shows for the same
rejection — no log-diving required.

### Example: malformed token

```json
{
  "received_azp": "",
  "received_sub": "",
  "valid":        false,
  "reason":       "invalid token: token is malformed: token contains an invalid number of segments"
}
```

### Example: expired token

Mint a short-lived token (admin UI → Clients → saas-backend → Advanced →
Access Token Lifespan = 5s), wait, then debug:

```bash
# Fresh
$ curl -sS http://localhost:8080/auth/debug -H "Authorization: Bearer $T" \
    | jq '{exp, expired, valid, reason}'
{
  "exp":     "2026-05-18T16:46:05Z",
  "expired": false,
  "valid":   true,
  "reason":  ""
}

# 8 seconds later
$ curl -sS http://localhost:8080/auth/debug -H "Authorization: Bearer $T" \
    | jq '{exp, expired, valid, reason}'
{
  "exp":     "2026-05-18T16:46:05Z",
  "expired": true,
  "valid":   false,
  "reason":  "token expired: token has invalid claims: token is expired"
}
```

`expired` flips before/after the wall-clock crosses `exp`. The client-side
`expired` check and the validator's verdict agree byte-for-byte — both use
strict `time.Now().After(exp)` with no leeway.

### Example: missing role (RBAC debug)

`testuser` has `realm_access.roles = ["user", "offline_access", ...]`;
`adminuser` has `["admin", "user", "offline_access", ...]`. A handler
gating on `identity.HasRole("admin")` will reject the first but accept the
second. Debug surfaces this directly:

```bash
$ curl -sS http://localhost:8080/auth/debug -H "Authorization: Bearer $TESTUSER_TOKEN" \
    | jq '.roles | contains(["admin"])'
false

$ curl -sS http://localhost:8080/auth/debug -H "Authorization: Bearer $ADMINUSER_TOKEN" \
    | jq '.roles | contains(["admin"])'
true
```

If the array is empty even for a user you expected to have roles, you've
probably forgotten the `roles` client scope on the client (Keycloak admin
UI → Clients → saas-backend → Client scopes → Add `roles` to defaults).

### Example: wrong audience

The endpoint surfaces `aud` exactly as the token carries it — it does NOT
fail validation on aud mismatch (the API's token validator only checks
iss/azp/exp/sub/signature). Use it to spot misconfiguration before a
downstream service that DOES enforce aud rejects you:

```bash
$ curl -sS http://localhost:8080/auth/debug -H "Authorization: Bearer $T" | jq .aud
[
  "account"
]
```

If you were expecting `aud=["saas-backend"]` because a downstream service
checks for it, this output tells you to add an Audience mapper on the
realm or client. Empty `[]` means no audience mapper is firing at all —
the most common case with bare Keycloak defaults.

### From the playground itself

Browser console (with the playground open and a token in `sessionStorage`):

```js
fetch("/auth/debug", {
  headers: { Authorization: "Bearer " + sessionStorage.kc_dev_access_token }
}).then(r => r.json()).then(console.table);
```

### Constraints (mirrors the rest of the playground)

- **Mounted only when `DEV_PLAYGROUND_ENABLED=true`.** Default is `false`. Production deployments simply don't set it, and the route 404s.
- **Surfaces unverified claim values** (`received_azp`, `received_sub`) so you can debug tampered/forged tokens. That's safe because everything in the response is data the caller already has (their own token + the API's public expected values). No new secrets exposed.
- **No new auth bypass.** The endpoint never grants access to anything — it only reports. Protected endpoints still require a valid bearer.

---

## Limitations

- **Local dev only.** Designed to be reached at `http://localhost:8080/dev/auth`.
  Running behind a reverse proxy would require updating the Keycloak client's
  `redirectUris` and `webOrigins`.
- **No user-list management.** The playground logs you in as whoever you
  authenticate via Keycloak. To add/remove users, use Keycloak's admin UI
  at `http://localhost:8081`.
- **No interactive request builder.** Right now there's a single **GET /me**
  button. Extending to arbitrary endpoints is straightforward — see the
  `callMe` function in [auth.js](../web/dev/auth.js).
- **The page itself isn't authenticated.** Anyone who reaches the URL can
  attempt to log in. That's fine because the URL only resolves locally.
- **Hand-rolled OIDC.** Robust enough for a dev tool. For production
  frontends, swap to [keycloak-js](https://www.keycloak.org/securing-apps/javascript-adapter)
  — the public-touchpoint surface (HTML element ids, `config.json` shape)
  stays the same.

---

## Troubleshooting

| Symptom                                                         | Cause / Fix                                                                                                |
|-----------------------------------------------------------------|------------------------------------------------------------------------------------------------------------|
| `/dev/auth` returns 404                                         | `DEV_PLAYGROUND_ENABLED` is not `true` in the API's environment. Set it in `.env`, regenerate, restart api. |
| Login redirects to Keycloak then back with `error=invalid_redirect_uri` | The `saas-dev-playground` client isn't registered (need `make realm-reset` after `make regen`), OR your browser is hitting the playground on a port that doesn't match `redirectUris`. |
| Token exchange fails with `unauthorized_client`                 | Same root cause as above — Keycloak doesn't know about the public client.                                  |
| Diagnostics: red dot on Keycloak                                | The Keycloak container isn't running or `KEYCLOAK_URL` in `/dev/auth/config.json` points to the wrong host. From the browser's view this URL must be reachable (i.e. `http://localhost:8081`, not `http://keycloak:8080`). |
| `/me` returns 401 with `key not found: kid`                     | The API's JWKS cache is stale (typically after a `make realm-reset` regenerated Keycloak's signing keys). The cache refreshes on kid-miss within ~30s. If it persists, `docker-compose restart api`. |
| Logout doesn't clear the Keycloak session                       | Browsers sometimes preserve the Keycloak SSO cookie. Open Keycloak's admin UI → Sessions → revoke. Or use a private tab. |

For non-playground troubleshooting, see [KEYCLOAK_SETUP.md §9](./KEYCLOAK_SETUP.md#9-troubleshooting).

---

## Disabling the playground

Either:

```json
// config/project.json
"features": { "dev_playground": false, ... }
```

then `make regen` + restart the API, **or** override at runtime:

```bash
DEV_PLAYGROUND_ENABLED=false docker-compose up -d --no-deps api
```

The `saas-dev-playground` Keycloak client remains in the realm export until
the feature flag is flipped back. That's harmless — Keycloak just keeps a
registered-but-unused public client — but if you want it gone, regenerate
and re-import.
