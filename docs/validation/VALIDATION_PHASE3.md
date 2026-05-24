# Phase 3 Validation — Sprint 3 sign-off

**Date:** 2026-05-17
**Status:** **PASS**
**Scope:** Full E2E from a fresh `docker-compose down -v` through Keycloak-issued token → /me auto-provision → stable local ID → no duplicate rows.

---

## 1. Stack health

```
NAME                     STATUS                    PORTS
saas-api                 Up (no healthcheck)       0.0.0.0:8080->8080/tcp
saas-keycloak            Up 4 minutes (healthy)    8443/tcp, 9000/tcp, 0.0.0.0:8081->8080/tcp
saas-keycloak-postgres   Up 4 minutes (healthy)    0.0.0.0:5433->5432/tcp
saas-postgres            Up 35 minutes (healthy)   0.0.0.0:5432->5432/tcp
```

All four containers are up. `postgres`, `keycloak-postgres`, and `keycloak` report `healthy` via their docker healthchecks. `saas-api` has no docker healthcheck defined (intentional — protected by `depends_on: keycloak: condition: service_healthy`), but its readiness was verified by:

- Application log `auth provider ready (keycloak realm=saas)` — JWKS fetched successfully at startup
- `GET /health` → `{"status":"ok"}`
- `GET /me` returning 200 with valid identity (see §4)

**Container boot order honored:** `saas-keycloak-postgres` (healthy) → `saas-keycloak` (healthy) → `saas-api` (started). API does not boot until Keycloak's `/health/ready` returns 200.

---

## 2. Realm auto-import verification

The realm `saas` is imported on first Keycloak boot from the bind-mounted `deploy/keycloak/realm-export.json`, which is itself generated from `config/project.json` + `.env` by `cmd/bootstrap`.

**Realm settings (live via Admin API `/admin/realms/saas`):**

```json
{
  "realm": "saas",
  "displayName": "lightweight-saas-backend",
  "enabled": true,
  "accessTokenLifespan": 3600,
  "sslRequired": "none",
  "bruteForceProtected": true
}
```

**Roles (live via `/admin/realms/saas/roles`):**

```
default-roles-saas    (built-in)
uma_authorization     (built-in)
offline_access        (built-in)
user                  (realm role: user)         <- from project.json
admin                 (realm role: admin)        <- from project.json
```

**Clients (live via `/admin/realms/saas/clients`):**

```
saas-backend  (direct_grants=true)               <- from project.json
```

**Users (live via `/admin/realms/saas/users`):**

```
testuser   (testuser@test.com,  firstName=Testuser)   <- seed
adminuser  (adminuser@test.com, firstName=Adminuser)  <- seed
```

All four artefacts (realm, client, custom roles, seed users) are present with the expected attributes.

**OIDC discovery doc returns:**

```json
{
  "issuer":          "http://localhost:8081/realms/saas",
  "jwks_uri":        "http://localhost:8081/realms/saas/protocol/openid-connect/certs",
  "token_endpoint":  "http://localhost:8081/realms/saas/protocol/openid-connect/token"
}
```

---

## 3. E2E flow — token acquisition → /me round-trip

### 3.1 Token via Direct Access Grants

```
POST http://localhost:8081/realms/saas/protocol/openid-connect/token
  client_id=saas-backend
  client_secret=saas-backend-secret
  grant_type=password
  username=testuser
  password=password

→ HTTP 200, access_token (length 1135 chars), 737ms
```

### 3.2 Token claims (decoded)

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
  "exp":                1779009288
}
```

`iss` and `azp` match the values the API expects (`KEYCLOAK_URL=http://localhost:8081`, `KEYCLOAK_CLIENT_ID=saas-backend`). `sub` is the canonical Keycloak UUID for the principal.

### 3.3 First /me call — auto-provision

```
GET /me  Authorization: Bearer <token>
→ 200, 24ms
{
  "id": 1,
  "keycloak_sub": "fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc",
  "email": "testuser@test.com",
  "username": "testuser",
  "created_at": "2026-05-17T08:14:48.642597402Z",
  "updated_at": "2026-05-17T08:14:48.642597402Z"
}
```

Local row JIT-provisioned by `Service.EnsureUser` on first call. `created_at == updated_at` — confirms zero reconciliation Updates after Create.

### 3.4 Second + third /me calls — reuse, no insert

```
GET /me (#2) → 200, 10ms, id=1
GET /me (#3) → 200, 12ms, id=1
```

Warm-path latency is **~10ms** (FindBySub + serialize). Cold-path is 24ms (FindBySub returns nil → Create → return).

### 3.5 Stable-ID assertion

```
id1=1  id2=1  id3=1
PASS — STABLE
```

### 3.6 DB state after testuser

```
 count: 1
 id |             keycloak_sub             |       email       | username
----+--------------------------------------+-------------------+----------
  1 | fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc | testuser@test.com | testuser
```

Exactly one row, as expected. No duplicates.

### 3.7 Second subject (adminuser) — distinct local row

```
GET /me with adminuser token → id=2 (distinct)

FINAL DB:
 id |       email        | username  |             keycloak_sub
----+--------------------+-----------+--------------------------------------
  1 | testuser@test.com  | testuser  | fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc
  2 | adminuser@test.com | adminuser | b1fdb328-7a00-4e49-bb91-9ba3c9febddc
```

Two distinct subjects → two distinct local IDs. No collision. Total E2E time: **1138ms**.

---

## 4. Structured auth logging

The `auth` event hook registered in `cmd/api/main.go` produced the following lines during the validation run (key/value-tagged for downstream parsers):

```
INFO  [ auth ] ok kind=token_validated sub=fbe56e3a-... method=GET path=/me dur=9.261007ms
INFO  [ auth ] ok kind=token_validated sub=fbe56e3a-... method=GET path=/me dur=146.374µs
INFO  [ auth ] ok kind=token_validated sub=fbe56e3a-... method=GET path=/me dur=118.791µs
INFO  [ auth ] ok kind=token_validated sub=b1fdb328-... method=GET path=/me dur=110.666µs
WARN  [ auth ] denied kind=validation_failed method=GET path=/me reason=missing required claim: sub dur=2.546994ms  (pre-fix attempt)
```

Auth events emit `kind`, `subject`, `method`, `path`, `reason`, and `dur` — the shape needed to plug Prometheus or OpenTelemetry in without touching middleware.

---

## 5. Latency summary

| Operation                                       | Latency       |
|-------------------------------------------------|---------------|
| Keycloak token endpoint (cold)                  | 737 ms        |
| `/me` #1 (auto-provision via EnsureUser)        | 24 ms         |
| `/me` #2 (reuse, second JWT validation)         | 10 ms         |
| `/me` #3 (warm)                                 | 12 ms         |
| **Auth middleware alone** (token_validated)     | **~150 µs**   |
| Full 8-step E2E (token + 3×/me + admin login + 2 DB queries) | 1138 ms |

The `~150 µs` per-request middleware latency is the steady state once the JWKS cache is warm; the first call after JWKS refresh costs ~10ms (the keyfunc kid-miss refresh now wired via `RefreshUnknownKID`).

---

## 6. Test suite — final tally

```
?   	cmd/api                              [no test files]
?   	cmd/bootstrap                        [no test files]
?   	internal/auth                        [no test files]
ok  	internal/auth/keycloak               0.878s   (11 tests)
ok  	internal/bootstrap                   0.443s   (21 tests)
ok  	internal/user                        0.246s   (9 tests)

TOTAL: 41 tests, 0 failures
```

`go build ./...` PASS. `go vet ./...` PASS. `go test ./...` PASS.

---

## 7. Issues found during validation (resolved before sign-off)

| # | Issue | Resolution |
|---|---|---|
| V1 | `.dockerignore` excluded `docs/`, breaking the Go build inside the API image (`internal/server/server.go` imports the generated swagger package as a blank-import side effect) | Added explicit `!docs/**` whitelist + comment in `.dockerignore` explaining the invariant |
| V2 | Port collisions with three of the host's other containers (`evolution_api:8080`, `infra-keycloak:8081`, `evolution_postgres:5433`) | User stopped the conflicting containers; documented as a risk for shared dev hosts |
| V3 | Keycloak rejected `_warning` top-level field in the generated realm export ("Unrecognized field") | Removed the field from the generator; DEV-ONLY notice now lives in `docs/architecture/bootstrap.md` and `config/project.json._meta` instead |
| V4 | Seeded users hit "Account is not fully set up" — realm's default user-profile schema requires firstName/lastName | Added `splitDisplayName(username) → (firstName, lastName)` helper in the generator; seeded users now satisfy the profile schema |
| V5 | API rejected tokens after Keycloak realm re-import: "key not found: kid ..." | Wired `RefreshUnknownKID` rate limiter on the multi-URL JWKS storage so the cache picks up emergency key rotations without restart |
| V6 | API rejected tokens with "invalid issuer" — token's `iss` was `localhost:8081` (host-facing) but API was deriving expected issuer from `keycloak:8080` (docker-internal) | Split the env vars in docker-compose: `KEYCLOAK_URL=http://localhost:8081` (drives expected iss) and `KEYCLOAK_JWKS_URL=http://keycloak:8080/...` (drives JWKS fetch over the docker network) |
| V7 | Tokens lacked `sub` claim — my generator overrode `defaultClientScopes` with a list that omitted the `basic` scope (which carries the sub mapper) | Removed the `defaultClientScopes` override entirely so Keycloak applies its realm-default scope set (basic + email + profile + roles + web-origins) |
| V8 | `rm -v` did not wipe the named `keycloak_postgres_data` volume; Keycloak skipped re-import because the realm still existed in its DB | Used `docker volume rm` directly. The Makefile `realm-reset` target already does this correctly. |

---

## 8. Constraints from the Phase 3 brief — adherence audit

| Constraint                                          | Status |
|-----------------------------------------------------|--------|
| Preserve auth/business separation                   | OK — `internal/user` has zero imports from `internal/auth/keycloak` |
| Provider abstraction untouched                      | OK — `auth.AuthProvider` interface unchanged since Phase 2 |
| No Keycloak imports in `user/`                      | OK — verified via `grep -r keycloak internal/user/` returns nothing |
| No dual auth paths                                  | OK — `/register`, `/login`, `internal/auth/jwt.go`, HS256 secret, all deleted |
| No feature flags for legacy JWT                     | OK — no `if AuthProvider == "hs256"` etc. anywhere |
| Delete legacy paths completely after validation     | OK — done in Phase 3.7-3.12 before this validation ran |
| Maintain bootstrap as source-of-truth               | OK — `.env`, `.env.example`, `realm-export.json`, `project.schema.json` are all regenerated from `config/project.json` + `.env` secrets |
| Idempotent EnsureUser                               | OK — verified by `id1==id2==id3` and `count(*) == 1` |

---

## 9. Sprint 3 — COMPLETE

The Sprint 1 brief's checklist is fully satisfied:

- [x] Docker Compose: api + postgres + keycloak (+ dedicated keycloak postgres)
- [x] Keycloak local environment
- [x] Realm configuration (auto-imported from generated `realm-export.json`)
- [x] Client configuration (`saas-backend`)
- [x] Roles: `admin`, `user`
- [x] OIDC setup (Direct Access Grants enabled for dev, Authorization Code stays for browsers)
- [x] JWT validation using JWKS (with kid-miss auto-refresh)
- [x] Replace custom JWT validation middleware with provider abstraction
- [x] Keep existing protected endpoints functional (`/me`)
- [x] Preserve Swagger (regenerated for the new endpoint surface)
- [x] Preserve tests (41 tests, all green)
- [x] Environment variables: `KEYCLOAK_URL`, `REALM`, `CLIENT_ID`, `CLIENT_SECRET`, `JWKS_URL`
- [x] Startup validation: fail fast on missing env (config/config.go `Validate()`)
- [x] Structured logging for auth failures (AuthEvent + EventHook)

Sprint 3 closes here. Further work tracked in the open risks section of [PHASE3_BREAKING_CHANGE.md](../architecture/PHASE3_BREAKING_CHANGE.md).
