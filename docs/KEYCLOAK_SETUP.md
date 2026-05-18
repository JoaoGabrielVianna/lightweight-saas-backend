# Keycloak Setup

Onboarding guide for the Keycloak-based authentication layer of
`lightweight-saas-backend`. Follow it top-to-bottom from a fresh clone and
you will end with a working stack, a token in your terminal, and a 200
response from `/me`.

Tested against the state of the repo at commit `Sprint 3` (Phase 3 sign-off,
[VALIDATION_PHASE3.md](./VALIDATION_PHASE3.md)).

---

## 1. Overview

### Why Keycloak

Sprint 1 of this project replaced a homegrown HS256 JWT issuer with Keycloak
because:

- **Single identity provider, many apps.** The same realm can serve this
  Go backend, a future frontend, a mobile app, an admin panel — every
  consumer validates against the same JWKS endpoint.
- **No password handling in business code.** Keycloak owns bcrypt, password
  policies, account lockout, password reset, MFA, social login. The Go API
  stops worrying about all of it.
- **Provider portability.** The codebase depends on an
  [`auth.AuthProvider`](../internal/auth/provider.go) interface, not on
  Keycloak. Swapping to Auth0/Supabase/Clerk later is a new provider
  implementation plus a wiring change in `cmd/api/main.go` — no business
  code changes.

### Architecture decision — split identities

There are **two distinct user concepts**, and they are kept on opposite
sides of an interface boundary:

| Concept              | Owned by  | Identifier                | Lives in                  |
|----------------------|-----------|---------------------------|---------------------------|
| **Auth identity**    | Keycloak  | `sub` (UUID, opaque)      | Keycloak's realm DB       |
| **Business identity**| this API  | `users.id` (uint, FK target) | App's `users` table    |

The link between them is a single column: `users.keycloak_sub`
(unique-indexed). Every protected request travels this path:

```
┌──────────────┐                                                    ┌─────────────────┐
│   Frontend   │                                                    │     Keycloak    │
│   / curl     │ 1. POST /token  (Authorization Code or password grant) ─►│  (realm=saas)  │
└──────┬───────┘                                                    └────────┬────────┘
       │                                                                    │
       │ 2. access_token (JWT, RS256, kid=...)                              │
       │◄───────────────────────────────────────────────────────────────────┘
       │
       │ 3. GET /me   Authorization: Bearer <jwt>
       ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                              this API (Go)                                   │
│                                                                              │
│   ┌──────────────────────────┐        ┌──────────────────────────────────┐   │
│   │  auth.RequireAuth        │        │  keycloak.Provider                │   │
│   │  (middleware)            │ ─────► │  - fetch JWKS, cache              │   │
│   │  - extract Bearer        │        │  - verify signature (RS256)       │   │
│   │  - call ValidateToken    │ ◄───── │  - check iss / azp / exp          │   │
│   │  - StoreIdentity(ctx,id) │        │  - project claims → *auth.Identity│   │
│   └────────┬─────────────────┘        └──────────────────────────────────┘   │
│            │                                                                 │
│            │ ctx now holds *auth.Identity{Subject, Email, Username, Roles}   │
│            ▼                                                                 │
│   ┌──────────────────────────┐        ┌──────────────────────────────────┐   │
│   │  user.Handler.Me         │        │  user.Service.EnsureUser          │   │
│   │  - IdentityFrom(c)       │ ─────► │  - FindBySub(id.Subject)          │   │
│   │  - service.EnsureUser    │        │     ├ nil → Create new local row  │   │
│   │  - return UserResponse   │ ◄───── │     └ found → Update if drift     │   │
│   └──────────────────────────┘        └──────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────────────┘
       │
       │ 4. 200 OK { id, keycloak_sub, email, username, ... }
       ▼
   Frontend / curl
```

Two key invariants this enforces:

1. **`internal/user/*` never imports Keycloak.** Verified by grep; failing
   that check should fail CI in the future.
2. **The local `users.id` is stable.** Re-logging in returns the same row;
   email/username drift in Keycloak is reconciled in place, but the
   primary key never changes — so foreign keys in other tables stay valid.

### Phased migration history

This Sprint 3 state is the end of a three-phase migration documented in
[migrations/PHASE3_BREAKING_CHANGE.md](./migrations/PHASE3_BREAKING_CHANGE.md).
Read it if you want to know why some old endpoints (`/register`, `/login`)
are gone and why the `users` table no longer has a `password` column.

---

## 2. Environment variables

Everything Keycloak-related is configured through `.env`. The file is
gitignored; `.env.example` is committed as the template. Both files are
regenerated by `cmd/bootstrap` from `config/project.json` + the existing
`.env`, so direct hand-editing of `.env` is fine for short-lived overrides
but will be lost on `make regen`.

### Required at API startup

The Go `config.LoadConfig().Validate()` function calls `log.Fatal` if any
of these are missing — the API will refuse to start, by design.

| Variable                  | Purpose                                                  | Example                                | Default | Risk if wrong |
|---------------------------|----------------------------------------------------------|----------------------------------------|---------|---------------|
| `DB_URL`                  | Postgres DSN the API connects to.                        | `postgres://postgres:postgres@localhost:5432/lightweight_saas_backend_db?sslmode=disable` | none | API fatal at boot. |
| `KEYCLOAK_URL`            | URL clients use to reach Keycloak. Used by the API to derive the **expected `iss` claim** in tokens, so it must match what's actually in the token. | `http://localhost:8081`                | none    | All tokens rejected with `invalid issuer`. |
| `KEYCLOAK_REALM`          | Realm name the API trusts.                               | `saas`                                 | none    | All tokens rejected. |
| `KEYCLOAK_CLIENT_ID`      | Primary client id. Used as fallback `azp` whitelist if `KEYCLOAK_ALLOWED_CLIENT_IDS` is unset, and as the lookup key for `resource_access.<client>.roles`. | `saas-backend`           | none    | Tokens for other clients rejected (in single-client mode). |
| `KEYCLOAK_ALLOWED_CLIENT_IDS` | Comma-separated whitelist of token-issuing client ids (matched against the `azp` claim). When unset, defaults to `{KEYCLOAK_CLIENT_ID}`. Set to `saas-backend,saas-dev-playground` for local dev with the playground; keep to just `saas-backend` (or omit) in production. | `saas-backend,saas-dev-playground` (dev) / `saas-backend` (prod) | derived from `KEYCLOAK_CLIENT_ID` | If a client used by the frontend isn't listed, all tokens it issues are rejected with `azp ... is not in the allowed-client set`. Adding `*` does NOT work — there's no wildcard. |
| `KEYCLOAK_JWKS_URL`       | Where the API fetches signing keys. Override in Docker so the API uses the internal hostname; in dev outside Docker the default derived from `KEYCLOAK_URL+REALM` is fine. | `http://keycloak:8080/realms/saas/protocol/openid-connect/certs` (Docker) or empty (host) | derived from `KEYCLOAK_URL` + `KEYCLOAK_REALM` if empty | If unreachable, API fatal at boot (JWKS fetch is blocking). |

### Optional but commonly set

| Variable                  | Purpose                                                  | Example                                | Default | Risk if wrong |
|---------------------------|----------------------------------------------------------|----------------------------------------|---------|---------------|
| `KEYCLOAK_CLIENT_SECRET`  | Confidential client secret. Used by clients to call Keycloak's token endpoint (e.g. password grant in `make auth-test`). Not used by the API for token validation. | `saas-backend-secret` | empty | Token endpoint rejects requests with `unauthorized_client`. |
| `SEED_USER_PASSWORD`      | Shared password for all `seed_users[]` entries in `config/project.json`. Consumed by `cmd/bootstrap` when generating `realm-export.json`. | `password` | `password` | Seeded users have a wrong password — login fails. |
| `KEYCLOAK_ADMIN`          | Keycloak's bootstrap admin username (consumed by the Keycloak container at first start). | `admin` | none | Admin UI unreachable. |
| `KEYCLOAK_ADMIN_PASSWORD` | Keycloak's bootstrap admin password.                     | `admin` (DEV ONLY)                     | none    | Admin UI unreachable. |
| `DEV_PLAYGROUND_ENABLED`  | Mount the DEV-ONLY auth playground at `/dev/auth`. See [DEV_AUTH_PLAYGROUND.md](DEV_AUTH_PLAYGROUND.md). | `true` (local), `false` (anywhere else) | `false` | If `true` in production, the playground is exposed. |
| `DEV_PLAYGROUND_CLIENT_ID`| Public OIDC client id used by the playground. Matches the realm-imported client. | `saas-dev-playground`               | `saas-dev-playground` | Mismatch → login redirects fail with `unauthorized_client`. |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` | App postgres init.            | `postgres` / `postgres` / `lightweight_saas_backend_db` | none | Postgres init fails. |
| `KC_DB_USER`, `KC_DB_PASSWORD`, `KC_DB_NAME` | Keycloak postgres init.                  | `keycloak` / `keycloak` / `keycloak`   | none    | Keycloak can't connect to its DB. |
| `PORT`                    | API HTTP port (inside container; published to host).     | `8080`                                 | `8080`  | Container listens on wrong port. |
| `GIN_LOG_ENABLED`         | Show Gin framework debug logs.                           | `true` / `false`                       | `true`  | Noisy logs. |
| `GIN_ACCESS_LOG_ENABLED`  | Log every HTTP request.                                  | `true` / `false`                       | `true`  | Loses request audit trail. |

### Complete `.env` example

This is what `cmd/bootstrap` produces for a fresh clone with the default
`config/project.json`:

```dotenv
# Auto-generated by `make init` / cmd/bootstrap. Edit config/project.json
# (and re-run `make regen`) rather than editing this file by hand.
# Secrets are sourced from this .env at regeneration time and preserved.

# =====================================================
# APPLICATION DATABASE
# =====================================================
POSTGRES_USER=postgres
POSTGRES_PASSWORD=postgres
POSTGRES_DB=lightweight_saas_backend_db
DB_URL=postgres://postgres:postgres@localhost:5432/lightweight_saas_backend_db?sslmode=disable

# =====================================================
# KEYCLOAK DATABASE
# =====================================================
KC_DB_USER=keycloak
KC_DB_PASSWORD=keycloak
KC_DB_NAME=keycloak

# =====================================================
# KEYCLOAK ADMIN BOOTSTRAP
# !!! DEV-ONLY DEFAULTS — rotate before any non-local deployment.
# =====================================================
KEYCLOAK_ADMIN=admin
KEYCLOAK_ADMIN_PASSWORD=admin

# =====================================================
# KEYCLOAK CLIENT CONFIGURATION
# =====================================================
KEYCLOAK_URL=http://localhost:8081
KEYCLOAK_REALM=saas
KEYCLOAK_CLIENT_ID=saas-backend
KEYCLOAK_CLIENT_SECRET=saas-backend-secret
KEYCLOAK_JWKS_URL=

# =====================================================
# SEED USER PASSWORD (shared across all seed users defined in project.json)
# =====================================================
SEED_USER_PASSWORD=password

# =====================================================
# APP
# =====================================================
PORT=8080
GIN_LOG_ENABLED=true
GIN_ACCESS_LOG_ENABLED=true
```

Inside `docker-compose.yml` the API container's `environment:` block
**overrides** `KEYCLOAK_URL` and `KEYCLOAK_JWKS_URL` so the API talks to
Keycloak over the docker network (`keycloak:8080`) while still accepting
tokens whose `iss` claim is the host-facing URL (`localhost:8081`). This
is intentional — see the troubleshooting section §9 for the full story.

---

## 3. Bootstrap process

The bootstrap layer ([cmd/bootstrap](../cmd/bootstrap/main.go) +
[internal/bootstrap](../internal/bootstrap/)) keeps a single
source-of-truth in `config/project.json` and regenerates every downstream
artefact from it. Hand-editing `.env`, `.env.example`, or
`deploy/keycloak/realm-export.json` works but those edits are **lost on
the next `make regen`** — change `config/project.json` instead.

### Quickstart — `make init`

```bash
make init                    # interactive prompts, then regenerates
# or:
go run ./cmd/bootstrap       # equivalent
```

Press Enter at any prompt to accept the `[default]`.

### Sample interactive session

```
$ make init
Bootstrap — interactive mode. Press Enter to accept the [default] value.

--- project ---
Project name [lightweight-saas-backend]:
Environment (local/dev/staging/prod) [local]:

--- auth (Keycloak) ---
Realm [saas]:
Client ID [saas-backend]:
Admin username [admin]:
Admin email [admin@local.dev]:

--- secrets (stored in .env, not committed) ---
Client secret [saas-backend-secret]:
Admin password [admin]:
Seed user password (shared) [password]:

--- ports ---
API port [8080]:
Postgres port [5432]:
Keycloak port [8081]:
Keycloak Postgres port [5433]:

--- features ---
Enable google_login? [y/N]:
Enable mfa? [y/N]:
Enable multi_tenant? [y/N]:
Enable swagger? [Y/n]:
Enable seed_users? [Y/n]:

+ wrote /Users/.../config/project.json
+ regenerated .env, .env.example, config/project.schema.json, deploy/keycloak/realm-export.json
Next: make up   # to start the stack
```

### Non-interactive regeneration

After hand-editing `config/project.json` (or after pulling a change to
the bootstrap generators), run:

```bash
make regen          # rebuilds the four generated files in place
```

This skips prompts and reads existing `.env` to preserve secrets.

### Generated files (owned by bootstrap — do not hand-edit)

| File                                       | Generator function in `internal/bootstrap/generate.go` | Purpose |
|--------------------------------------------|--------------------------------------------------------|---------|
| `.env`                                     | `writeEnv` (with current secrets)                      | Runtime config for both Docker and bare-metal runs |
| `.env.example`                             | `writeEnv` (with placeholder secrets)                  | Template for new clones |
| `config/project.schema.json`               | `writeSchemaFile`                                      | Mirror of the embedded JSON Schema — used by IDEs for autocomplete |
| `deploy/keycloak/realm-export.json`        | `writeRealmExport`                                     | Imported by Keycloak on first boot |

### Editable (committed) source-of-truth

| File                          | Purpose                                              |
|-------------------------------|------------------------------------------------------|
| `config/project.json`         | Single source-of-truth — non-secret project description |
| `internal/bootstrap/schema/project.schema.json` | The embedded JSON Schema — canonical |

See [docs/bootstrap.md](./bootstrap.md) for the full design.

---

## 4. Docker startup

### One-shot

```bash
make up
# or:
docker-compose up -d --build
```

`make up` is the supported entrypoint. It calls `docker-compose up -d --build`
under the hood; `--build` ensures the API image picks up local code changes.

### Expected containers

| Container               | Image                          | Port (host → container)         | Notes |
|-------------------------|--------------------------------|----------------------------------|-------|
| `saas-postgres`         | `postgres:15-alpine`           | `5432 → 5432`                   | App DB |
| `saas-keycloak-postgres`| `postgres:15-alpine`           | `5433 → 5432`                   | Keycloak's own DB (isolated) |
| `saas-keycloak`         | `quay.io/keycloak/keycloak:26.0` | `8081 → 8080`                 | Admin UI + OIDC endpoints |
| `saas-api`              | locally built from `Dockerfile`| `8080 → 8080`                   | The Go API |

### Verify healthy

```bash
$ docker-compose ps
NAME                     STATUS                    PORTS
saas-api                 Up 8 minutes              0.0.0.0:8080->8080/tcp
saas-keycloak            Up 4 minutes (healthy)    0.0.0.0:8081->8080/tcp
saas-keycloak-postgres   Up 4 minutes (healthy)    0.0.0.0:5433->5432/tcp
saas-postgres            Up 4 minutes (healthy)    0.0.0.0:5432->5432/tcp
```

`saas-api` has **no docker healthcheck defined** (intentional — start
order is enforced by `depends_on: keycloak: condition: service_healthy`).
Verify it's actually serving:

```bash
$ curl -fsS http://localhost:8080/health
{"status":"ok"}
```

### Common startup failures

| Symptom                                                         | Cause                                                         | Fix |
|-----------------------------------------------------------------|---------------------------------------------------------------|-----|
| `Bind for 0.0.0.0:5432 failed: port is already allocated`       | Another container or local postgres holds the port             | `lsof -nP -iTCP:5432 -sTCP:LISTEN` to find it; stop it or change `ports.postgres` in `config/project.json` |
| `Bind for 0.0.0.0:5433 failed: port is already allocated`       | Another keycloak/postgres stack on this host                  | Same; change `ports.keycloak_postgres` in `config/project.json` and `make regen` |
| `Bind for 0.0.0.0:8080/8081 failed`                             | An existing API / Keycloak                                    | Same; change `ports.api` / `ports.keycloak` |
| `dependency failed to start: container saas-keycloak is unhealthy` | Realm import error — bad `realm-export.json` (e.g. unknown fields) | `docker logs saas-keycloak` to read the parser error, fix the generator, `make regen`, `make realm-reset` |
| `no required module provides package .../docs` during API image build | `.dockerignore` excluded the generated swagger package | Already fixed in the repo (`!docs/**` whitelist); if you see it again, re-check `.dockerignore` |

### Stack lifecycle commands

The Makefile exposes five lifecycle targets, ordered from least-to-most
destructive. Pick by intent, not by force-of-habit.

| Command          | What it does                                                | Keeps DB? | Use when                                                  |
|------------------|-------------------------------------------------------------|-----------|-----------------------------------------------------------|
| `make stop`      | `docker-compose stop` — pauses containers, keeps everything | YES       | Pause work for the day; resume quickly tomorrow.          |
| `make start`     | `docker-compose start` — resumes stopped containers         | YES       | Resume after `make stop`.                                 |
| `make down`      | Removes containers + network; preserves named volumes       | YES       | Clean container/network state without losing DB rows.     |
| `make purge`     | Removes containers + volumes + network + bin/ + api image. Prompts `y/N`. | NO | Want a guaranteed-clean baseline. Asks before nuking. |
| `make reset-dev` | `purge` + rebuild + start, in one shot. Single `y/N` prompt. | NO       | **Rescue command** when Keycloak is wedged, JWKS is stale, a migration is broken, or a volume is corrupted. |

**Decision tree for a broken environment:**

```
Stack not responding?
├─ Containers running? `docker-compose ps`
│  └─ No → `make start` (if stopped) or `make up` (if removed)
├─ Containers running but Keycloak rejecting tokens?
│  └─ `docker logs saas-keycloak | tail` → fix realm; `make realm-reset`
├─ API rejecting tokens with "key not found"?
│  └─ `docker-compose restart api` (JWKS will re-fetch)
├─ DB schema is off / migration failed mid-flight?
│  └─ `make reset-dev` (wipes everything, rebuilds, restarts)
└─ Nothing else worked, I just want a clean slate
   └─ `make reset-dev`
```

`purge` and `reset-dev` always show:

```
⚠️  This will DELETE all local data and docker volumes.
Continue? [y/N]
```

Anything other than `y`, `Y`, `yes`, `YES` aborts the command without
touching anything.

---

## 5. Keycloak realm import

### How `realm-export.json` is consumed

`deploy/keycloak/` is bind-mounted into the Keycloak container at
`/opt/keycloak/data/import` (read-only). The container starts with
`start-dev --import-realm`, which:

1. Walks the import directory.
2. For each `*.json` realm file, imports it **only if the realm doesn't
   already exist in Keycloak's DB**.
3. Skips the file silently if the realm exists, logging:
   ```
   INFO  [...] Realm 'saas' already exists. Import skipped
   ```

This is important: **a freshly generated `realm-export.json` will NOT
overwrite a Keycloak that has already booted once.** To re-import, you
must wipe Keycloak's database first.

### How `realm-export.json` is generated

`writeRealmExport` in [internal/bootstrap/generate.go](../internal/bootstrap/generate.go)
composes the file from:

- `config/project.json` → realm name, client id, roles, ports, seed user
  list
- `.env` (via `LoadSecrets`) → client secret, seed user password

It deliberately does **not** override `defaultClientScopes` so Keycloak
applies its built-in scope set (which includes the `basic` scope that
attaches the `sub` mapper — without it tokens would lack `sub` and the
API would reject them).

### When to reset the realm

You must re-import the realm whenever you change anything in
`config/project.json` that affects Keycloak shape (roles, client config,
seed user list, realm name) **and** want those changes reflected in a
running Keycloak.

```bash
# regenerate the export from the new project.json
make regen

# nuke Keycloak DB and re-import on next boot
make realm-reset
```

`make realm-reset` runs (with a `y/N` confirmation):

```
docker-compose stop keycloak keycloak-postgres
docker volume rm <project>_keycloak_postgres_data
docker-compose up -d keycloak-postgres keycloak
```

### Common gotcha

`docker-compose rm -v` only removes **anonymous** volumes. The named
`keycloak_postgres_data` volume is NOT removed by it. Use
`docker volume rm <project>_keycloak_postgres_data` directly, or use
`make realm-reset` which does it for you.

---

## 6. Validate Keycloak is working

### Admin UI

Open <http://localhost:8081> in a browser.

Login with the bootstrap admin credentials from `.env`
(`KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD`, defaults `admin` / `admin`).

In the admin console, switch the realm dropdown (top-left) from `Master`
to **`saas`**.

### Expected contents (sanity check)

| Section                        | Expected value                                             |
|--------------------------------|------------------------------------------------------------|
| **Realm settings → General** → Realm ID | `saas`                                            |
| **Realm settings → General** → Display name | `lightweight-saas-backend`                    |
| **Clients**                    | `saas-backend` (Client authentication: ON, Direct access grants: ON) |
| **Realm roles**                | `admin`, `user` (alongside Keycloak built-ins) |
| **Users**                      | `testuser`, `adminuser`                                    |
| **Realm settings → Tokens** → Access token lifespan | `1 hour`                              |

### Programmatic sanity check (no browser)

```bash
# Get an admin token, then inspect roles + users in one shot
ADMIN=$(curl -fsS -X POST "http://localhost:8081/realms/master/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d 'client_id=admin-cli' -d 'username=admin' -d 'password=admin' -d 'grant_type=password' \
  | jq -r .access_token)

curl -fsS -H "Authorization: Bearer $ADMIN" http://localhost:8081/admin/realms/saas/roles \
  | jq -r '.[] | "  " + .name'

curl -fsS -H "Authorization: Bearer $ADMIN" http://localhost:8081/admin/realms/saas/users \
  | jq -r '.[] | "  " + .username + " <" + .email + ">"'
```

### OIDC discovery (the URL the API uses)

```bash
$ curl -fsS http://localhost:8081/realms/saas/.well-known/openid-configuration | jq '{issuer, jwks_uri, token_endpoint}'
{
  "issuer":         "http://localhost:8081/realms/saas",
  "jwks_uri":       "http://localhost:8081/realms/saas/protocol/openid-connect/certs",
  "token_endpoint": "http://localhost:8081/realms/saas/protocol/openid-connect/token"
}
```

If the JWKS endpoint returns a JSON Web Key Set with at least one key,
the API can validate tokens. The API verifies this at startup — if you
see `auth provider ready (keycloak realm=saas)` in `docker logs saas-api`,
JWKS fetch succeeded.

---

## 7. Obtain a token manually

The realm has Direct Access Grants enabled (dev convenience). Production
clients should use Authorization Code + PKCE instead.

### curl

```bash
TOKEN=$(curl -fsS -X POST "http://localhost:8081/realms/saas/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=saas-backend" \
  -d "client_secret=saas-backend-secret" \
  -d "grant_type=password" \
  -d "username=testuser" \
  -d "password=password" \
  | jq -r '.access_token')

echo "token length: ${#TOKEN}"
```

### httpie

```bash
http -f POST http://localhost:8081/realms/saas/protocol/openid-connect/token \
  client_id=saas-backend \
  client_secret=saas-backend-secret \
  grant_type=password \
  username=testuser \
  password=password
```

### Decode the token (no shared secret needed — base64 only)

```bash
PAYLOAD=$(echo "$TOKEN" | cut -d. -f2 | tr '_-' '/+')
case $((${#PAYLOAD} % 4)) in 2) PAYLOAD="$PAYLOAD==";; 3) PAYLOAD="$PAYLOAD=";; esac
echo "$PAYLOAD" | base64 -d 2>/dev/null | jq .
```

### Expected response shape

```json
{
  "access_token":       "eyJhbGciOi...",
  "expires_in":         3600,
  "refresh_expires_in": 1800,
  "refresh_token":      "...",
  "token_type":         "Bearer",
  "session_state":      "...",
  "scope":              "email profile"
}
```

### Expected claims in the access_token

```json
{
  "iss":                "http://localhost:8081/realms/saas",
  "azp":                "saas-backend",
  "sub":                "fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc",
  "preferred_username": "testuser",
  "email":              "testuser@test.com",
  "given_name":         "Testuser",
  "family_name":        "User",
  "realm_access":       { "roles": ["user"] },
  "exp":                1779009288,
  "iat":                1779005688
}
```

| Claim                 | Meaning |
|-----------------------|---------|
| `iss`                 | Issuer — must equal `KEYCLOAK_URL + /realms/ + KEYCLOAK_REALM` or the API rejects. |
| `azp`                 | Authorized party — the client that requested the token; must equal `KEYCLOAK_CLIENT_ID`. |
| `sub`                 | Subject — opaque Keycloak UUID, **the canonical identity** used by `EnsureUser`. |
| `preferred_username`  | Becomes `User.Username` after EnsureUser. |
| `email`               | Becomes `User.Email` after EnsureUser. |
| `realm_access.roles`  | Realm-level role list — `auth.Identity.Roles`. Use `identity.HasRole("admin")` in handlers. |
| `exp` / `iat`         | Expiry / issued-at — enforced by the JWT library. |

---

## 8. Test protected routes

```bash
TOKEN=...  # from §7

# First call — should provision a local row
curl -fsS -H "Authorization: Bearer $TOKEN" http://localhost:8080/me | jq .
```

Expected response:

```json
{
  "id": 1,
  "keycloak_sub": "fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc",
  "email":        "testuser@test.com",
  "username":     "testuser",
  "created_at":   "2026-05-17T08:14:48.642597402Z",
  "updated_at":   "2026-05-17T08:14:48.642597402Z"
}
```

Notice `created_at == updated_at`: no reconciliation update fired
because the new row's claims already match the token.

### Stable-ID guarantee

```bash
# Call /me three more times
for i in 1 2 3; do
  curl -fsS -H "Authorization: Bearer $TOKEN" http://localhost:8080/me | jq -r '.id'
done
# Expected output:
# 1
# 1
# 1
```

The same `id` is returned across calls and across processes for the same
Keycloak `sub`. This is the contract:

> For any Keycloak identity `S`, the local `users.id` returned for `S`
> never changes once provisioned, even under concurrent requests.

### How `EnsureUser` enforces idempotency

[`user.Service.EnsureUser`](../internal/user/service.go):

1. `FindBySub(id.Subject)` → returns existing row or `(nil, nil)`.
2. If `nil` → `Create` a new row. If `Create` fails because another
   concurrent request just inserted the same `sub` (DB unique constraint
   on `keycloak_sub`), re-read by sub and return that row.
3. If found and any claim (`email`, `username`) drifted since last login,
   `Update` in place. The `id` is never touched.

Tested by [`TestEnsureUser_RaceCondition_NeverDuplicates`](../internal/user/service_test.go)
which fires 50 goroutines at the service simultaneously and asserts
exactly one row gets created.

### Verify in the DB

```bash
docker exec saas-postgres psql -U postgres -d lightweight_saas_backend_db \
  -c "SELECT id, email, username, keycloak_sub FROM users;"
```

```
 id |       email        | username  |             keycloak_sub
----+--------------------+-----------+--------------------------------------
  1 | testuser@test.com  | testuser  | fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc
  2 | adminuser@test.com | adminuser | b1fdb328-7a00-4e49-bb91-9ba3c9febddc
```

Two logins → two rows. Same user logging in 100 times → one row.

### Convenience: `make auth-test`

The Makefile bundles steps 7 + 8 into a single command:

```bash
make auth-test     # token → /me, end to end. Requires curl + jq.
```

Source: [scripts/auth-test.sh](../scripts/auth-test.sh).

---

## 9. Troubleshooting

These are all issues hit during the actual Phase 3 validation
([VALIDATION_PHASE3.md §7](./VALIDATION_PHASE3.md#7-issues-found-during-validation-resolved-before-sign-off)).
Each entry is shaped Symptom / Cause / Fix.

### 9.1 API logs `key not found: kid "..."` and rejects all tokens

**Symptom:** every `/me` returns 401; `docker logs saas-api` shows:
```
denied kind=validation_failed reason=invalid token: token is unverifiable:
  error while executing keyfunc: key not found: kid "..."
  failed keyfunc: could not read JWK from storage
```

**Cause:** Keycloak's signing keys rotated (typically because you wiped
its DB and re-imported the realm), and the API's JWKS cache hasn't
refreshed. The cache only refreshes hourly by default.

**Fix:** Already wired — `internal/auth/keycloak/jwks.go` configures
`RefreshUnknownKID` so the cache refreshes when an unknown `kid` shows
up (rate-limited to 1 refresh per 30s, burst 2). If you see this in
production, check that the rate limiter isn't being saturated by an
attacker spamming bogus kids — the API is correctly refusing in that
case. For dev, just retry the request after ~1s.

### 9.2 API logs `token has invalid issuer`

**Symptom:**
```
denied kind=validation_failed reason=invalid token: token has invalid claims: token has invalid issuer
```

**Cause:** the token's `iss` claim doesn't match the issuer the API
derives from `KEYCLOAK_URL + /realms/ + KEYCLOAK_REALM`. Most common
in Docker: the token was fetched at the host-facing URL
(`http://localhost:8081/realms/saas`) but the API was configured with
the docker-internal URL (`http://keycloak:8080/realms/saas`).

**Fix:** in `docker-compose.yml`, set:
- `KEYCLOAK_URL=http://localhost:8081` — drives expected `iss`, matches
  what clients see
- `KEYCLOAK_JWKS_URL=http://keycloak:8080/realms/<realm>/protocol/openid-connect/certs`
  — drives the API's own JWKS fetch over the docker network

Outside Docker, the default derivation works because there's only one
URL involved.

### 9.3 Tokens are issued but lack a `sub` claim

**Symptom:**
```
denied kind=validation_failed reason=missing required claim: sub
```

**Cause:** the client's `defaultClientScopes` was overridden with a list
that doesn't include the `basic` scope. The `basic` scope is what
attaches Keycloak's sub mapper to issued tokens.

**Fix:** don't override `defaultClientScopes` in the generator. The
generator already does the right thing — if you customize the realm
export, leave the field unset so Keycloak applies its built-in defaults
(`basic`, `email`, `profile`, `roles`, `web-origins`, `acr`).

### 9.4 Token endpoint returns `Account is not fully set up`

**Symptom:**
```json
{"error":"invalid_grant","error_description":"Account is not fully set up"}
```

**Cause:** the user is missing required profile fields. The default
Keycloak user-profile schema requires `firstName` and `lastName` for any
user with the `user` role.

**Fix:** the generator now derives `firstName` / `lastName` from the
seed username via `splitDisplayName`. If you're adding users via the
Admin UI or API, make sure firstName + lastName + email are all set.

### 9.5 `Bind for 0.0.0.0:NNNN failed: port is already allocated`

**Symptom:** `docker-compose up` aborts during container creation.

**Cause:** another process or container holds one of the host ports
(`5432`, `5433`, `8080`, `8081`).

**Fix:**
```bash
lsof -nP -iTCP:8081 -sTCP:LISTEN     # identify the holder
# either stop the conflicting process, or change ports.keycloak in
# config/project.json and `make regen`
```

### 9.6 Keycloak refuses realm import: `Unrecognized field "..."`

**Symptom:** `saas-keycloak` reports unhealthy; logs show
```
ERROR ... Unrecognized field "X" (class ...RealmRepresentation), not marked as ignorable
```

**Cause:** the realm-export.json has a top-level field Keycloak doesn't
know about. Keycloak's Jackson parser is strict — there is no "ignore
unknown" mode.

**Fix:** remove the offending field from the generator. If you need to
attach metadata, put it in `config/project.json._meta` (consumed by the
bootstrap layer, not by Keycloak).

### 9.7 Keycloak silently skips re-import: `Realm 'saas' already exists. Import skipped`

**Symptom:** you regenerated `realm-export.json` and restarted Keycloak,
but your changes aren't visible.

**Cause:** `start-dev --import-realm` only imports realms that don't
already exist. The realm survives container restarts because it lives in
the `keycloak_postgres_data` named volume.

**Fix:** use `make realm-reset` (which removes the named volume), then
`docker-compose up -d`. Note: `docker-compose rm -v` does NOT remove
named volumes — only anonymous ones.

### 9.8 API container build fails with `no required module provides package .../docs`

**Symptom:** during `docker-compose up --build`:
```
internal/server/server.go:4:2: no required module provides package github.com/.../docs
```

**Cause:** `.dockerignore` excluded the generated swagger package
(`docs/`). The server imports it as a blank-import for swagger UI.

**Fix:** already handled — `.dockerignore` has `!docs/**` whitelisting
the directory after the generic `*.md` exclusion. If you add new
exclusions, don't re-exclude `docs/`.

### 9.9 `unauthorized` on `/me` immediately after `make up`

**Symptom:** API healthy, Keycloak healthy, but `/me` returns 401 with
no specific reason in logs.

**Possible cause:** the API booted before Keycloak finished importing the
realm, so its JWKS cache is for a realm that doesn't exist yet.

**Fix:** restart the API container so it re-fetches:
```bash
docker-compose restart api
```
The `depends_on: keycloak: condition: service_healthy` clause should
prevent this in normal operation; if you see it, file a bug.

### 9.x Nothing works and I just want to start over

`make reset-dev` is the one-command rescue: it prompts for confirmation,
then wipes containers, volumes, the api image, and `bin/`, and rebuilds
and starts the whole stack from scratch. After it finishes, run
`make auth-test` to confirm health.

```bash
make reset-dev
# answer 'y'
# ... ~30s later ...
make auth-test
```

If `make reset-dev` itself fails, run `make doctor` to surface the
underlying issue (missing tools, dead docker daemon, port collisions).

### 9.10 `make auth-test` fails with `jq required`

**Symptom:**
```
- jq required
```

**Fix:** `brew install jq` (macOS) or `apt-get install jq` (Linux).
`make doctor` lists all required tools.

---

## 10. Production considerations

This setup is **dev-shaped**. Several defaults must change before any
non-local deployment.

### What to disable

| Dev convenience                        | Production replacement                                       |
|----------------------------------------|--------------------------------------------------------------|
| Seeded `testuser` / `adminuser`        | Don't include them — flip `features.seed_users` to `false` in `config/project.json`, regen, redeploy. Users provisioned via Keycloak's normal flows (admin invites, self-service, SSO). |
| Hardcoded admin / `admin@admin`        | Inject `KEYCLOAK_ADMIN_PASSWORD` from a secret store on first boot only; rotate via the admin UI; remove the env var afterwards. |
| Direct Access Grants (password grant)  | Disable on the `saas-backend` client. Browsers use Authorization Code + PKCE; backend-to-backend uses Service Accounts. |
| `KC_HOSTNAME_STRICT: "false"`          | Set a real hostname: `KC_HOSTNAME=https://auth.your-domain.com`. |
| `KC_HTTP_ENABLED: "true"`              | Terminate TLS in front of Keycloak (or let Keycloak terminate it). |
| Keycloak's `start-dev`                 | Use `start --optimized` after running `kc.sh build` once at image bake time. |
| `--import-realm` on every boot         | Import once via a one-shot job (e.g. Kubernetes Job); subsequent boots don't touch the realm. |
| `KEYCLOAK_CLIENT_SECRET=saas-backend-secret` (in repo) | Rotate to a high-entropy value, store in a secret manager (Vault, AWS Secrets Manager, Doppler). |
| `bruteForceProtected: true` (already on) | Keep it; tighten `failureFactor` and `waitIncrementSeconds` in realm settings. |

### What to add

| Capability         | What it looks like with this architecture |
|--------------------|--------------------------------------------|
| **Google / GitHub SSO** | Add an Identity Provider in the realm. Token claims gain `identity_provider`; no API code change needed. Flip `features.google_login` in `project.json` to drive the realm import. |
| **MFA**            | Realm-level Authentication Flow change. Token-validation behavior unchanged on the API side. Flip `features.mfa` for documentation parity. |
| **Multi-tenant**   | One realm per tenant, or one realm with `tenant_id` as a custom user attribute → custom mapper → claim → `auth.Identity.Raw["tenant_id"]`. The `users` table grows a `tenant_id` column. |
| **RBAC enforcement** | The provider already projects `realm_access.roles` and `resource_access.<client>.roles` into `identity.Roles`. Add an `auth.RequireRole("admin")` middleware (mirrors `RequireAuth`) and apply it to the route group. |
| **Observability**  | `auth.SetEventHook` accepts any function. Replace the default text logger in `cmd/api/main.go` with a Prometheus counter + OpenTelemetry span emitter. No middleware change. |
| **Refresh tokens** | Already issued by Keycloak. The API doesn't need to handle them — clients use Keycloak's `/token` endpoint with `grant_type=refresh_token`. |

### Non-negotiables before deploying

- `.env` (or its production equivalent) must NOT be in the repo.
- `KEYCLOAK_ADMIN_PASSWORD` and `KEYCLOAK_CLIENT_SECRET` must come from
  a secret manager, not env files copied to disk.
- `KEYCLOAK_URL` must use HTTPS.
- `KEYCLOAK_JWKS_URL` must be reachable from the API but not exposed to
  the internet if the API is internal.
- Audit Keycloak's `accessTokenLifespan` (currently 1 hour) against your
  threat model.

---

## 11. Final validation checklist

Run this end-to-end against any fresh clone or after any major env change.
All boxes must tick before declaring the integration healthy.

```
[ ] git clone && cd lightweight-saas-backend
[ ] make doctor                                # required tools present
[ ] make init      (or `make regen` if you trust the defaults)
[ ] make up                                    # builds api, pulls keycloak, starts all 4 containers
[ ] docker-compose ps                          # 3 healthy + saas-api running
[ ] curl -fsS http://localhost:8080/health     # → {"status":"ok"}
[ ] curl -fsS http://localhost:8081/realms/saas/.well-known/openid-configuration
                                               # → iss/jwks_uri/token_endpoint present
[ ] make auth-test                             # token acquired + /me returns 200
[ ] Re-run `make auth-test` twice              # same local `id` each time
[ ] docker exec saas-postgres psql -U postgres -d lightweight_saas_backend_db \
       -c 'SELECT COUNT(*) FROM users;'        # = 1 for one user, = 2 after adminuser logs in
[ ] docker logs saas-api 2>&1 | grep token_validated   # structured auth events present
```

If any box fails, jump to §9 with the symptom in hand. If nothing in §9
matches, capture `docker logs saas-keycloak` and `docker logs saas-api`
and file an issue.

---

## Related docs

- [VALIDATION_PHASE3.md](./VALIDATION_PHASE3.md) — Sprint 3 sign-off report (proof this all works on a fresh clone)
- [migrations/PHASE3_BREAKING_CHANGE.md](./migrations/PHASE3_BREAKING_CHANGE.md) — how this state replaced the legacy HS256 path
- [bootstrap.md](./bootstrap.md) — the bootstrap CLI and source-of-truth design
- [AUDITORIA_TECNICA.md](../AUDITORIA_TECNICA.md) — pre-Phase-3 technical audit (now partially stale; keep for historical context)
