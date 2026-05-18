# Phase 3 — Breaking change: Keycloak owns identity

**Date:** 2026-05-17
**Affects:** every deployment of `lightweight-saas-backend`
**Backward compatibility:** none. Phase 3 deletes legacy paths instead of feature-flagging them, per the project's "no dual auth paths" rule.

## What changed

### Identity ownership
- Local password storage is **gone**. Keycloak is the sole identity provider.
- `POST /register` and `POST /login` endpoints are **removed**. Clients obtain tokens directly from Keycloak via:
  - Authorization Code + PKCE (browsers)
  - Direct Access Grants (CLI tools, automated tests)
- Bearer tokens validated against the Keycloak JWKS endpoint are required for all protected routes.

### Local user table — schema breaks
| Column            | Phase 2                              | Phase 3                                 |
|-------------------|--------------------------------------|-----------------------------------------|
| `id`              | `uint`, primary key                  | unchanged                               |
| `email`           | `string`, unique index, not null     | `string`, **index** (no longer unique)  |
| `password`        | `string`, bcrypt, not null           | **REMOVED**                             |
| `keycloak_sub`    | —                                    | **NEW** `string`, unique index, not null |
| `username`        | —                                    | **NEW** `string`, not null              |
| `created_at`      | `time.Time`                          | unchanged                               |
| `updated_at`      | `time.Time`                          | unchanged                               |

The canonical identity is now `keycloak_sub` (the JWT `sub` claim — a Keycloak-managed UUID). The local `id` remains as the FK target for the rest of the platform; it's a stable internal handle, not a public identity.

### Code surface that changed
- `internal/auth/jwt.go` — **deleted** (was the HS256 issuer)
- `internal/auth/middleware.go` — replaced (was HS256-bound, now provider-agnostic via `RequireAuth(AuthProvider)`)
- `internal/user/service.go` — `Register` / `Login` / `GetByID` removed. Replaced with `EnsureUser(*auth.Identity)`.
- `internal/user/repository.go` — `FindByEmail` removed. Added `FindBySub`, `Update`.
- `internal/user/handler.go` — `Register`, `Login` handlers removed. `Me` rewritten to read `auth.Identity` from context and call `EnsureUser`.
- `internal/user/dto.go` — `RegisterRequest`, `LoginRequest`, `AuthResponse` removed.
- `internal/config/config.go` — `JWTSecret` field removed. All Keycloak fields now required at startup (fail-fast).
- `internal/database/database.go` — `seedDefaultUser` (bcrypt seed) removed. Keycloak seeds users via `deploy/keycloak/realm-export.json`.
- `cmd/api/main.go` — builds Keycloak provider, registers auth event hook, threads provider into the router.

## Database migration

Phase 3 is a **destructive** schema change. The migration story:

```
# 1. Stop everything
docker-compose down -v        # removes ALL postgres volumes — DATA LOSS

# 2. Bring up the new stack
docker-compose up -d

# 3. gorm AutoMigrate runs against an empty database
#    and creates the new `users` table with KeycloakSub + Username + Email + timestamps.

# 4. First /me call for each authenticated subject JIT-creates their local row.
```

**There is no in-place upgrade path** from Phase 2 to Phase 3. The legacy `users` rows held bcrypt password hashes that have no counterpart in the new model; users must re-authenticate via Keycloak. No data migration is provided because:

1. The dev environment only ever held the seeded `test@test.com` user.
2. No production deployments existed pre-Phase-3.
3. Adding a migration would require an HS256 → OIDC bridge that violates the "no dual auth paths" constraint.

If a future user base needs to be migrated, write a one-shot script that:
- creates Keycloak users from the legacy `users.email` (asks each user to set a fresh password via Keycloak's "forgot password" flow)
- on first `/me` for the migrated subject, joins by `email` and writes `keycloak_sub` into the existing row instead of creating a new one

That script is out of scope for Phase 3.

## Auto-provisioning semantics

Implemented in `Service.EnsureUser`:

```
identity.Subject empty           -> ErrInvalidIdentity
FindBySub(sub) returns nil       -> Create new local row from claims
FindBySub(sub) returns existing  -> diff email/username from claims:
                                       changed -> Update
                                       unchanged -> no-op
Create fails with unique conflict -> re-FindBySub (concurrent race) and return that
```

Uniqueness is enforced at the database level via `gorm:"uniqueIndex"` on `KeycloakSub`. The Go-level race recovery exists as defense-in-depth; the DB constraint is the actual guarantee that local rows never duplicate.

Empty claims are treated as "unknown", not "cleared": a token without an `email` claim will not wipe a previously-stored email. Reconciliation is additive.

## Environment variable changes

| Variable                | Phase 2 | Phase 3 |
|-------------------------|---------|---------|
| `JWT_SECRET`            | required (HS256 secret) | **removed** |
| `KEYCLOAK_URL`          | optional | **required** (fail-fast) |
| `KEYCLOAK_REALM`        | optional | **required** (fail-fast) |
| `KEYCLOAK_CLIENT_ID`    | optional | **required** (fail-fast) |
| `KEYCLOAK_JWKS_URL`     | optional | **required** (or derive from URL+REALM) |
| `KEYCLOAK_CLIENT_SECRET`| optional | optional (reserved for Admin-API features) |
| `SEED_USER_PASSWORD`    | n/a      | **new** — shared password for realm-imported seed users |

`config.LoadConfig` calls `Validate()` which `log.Fatal`s on any missing required field.

## Observability additions

`internal/auth/events.go` introduces `AuthEvent` + `SetEventHook`. `main.go` registers a default hook that writes structured key=value lines via the project logger. Downstream Prometheus / OpenTelemetry consumers can replace the hook without touching middleware.

| Event kind              | When emitted                                |
|-------------------------|---------------------------------------------|
| `token_validated`       | Provider successfully validated a token     |
| `validation_failed`     | Provider rejected the token                 |
| `missing_header`        | No `Authorization` header on protected route |
| `malformed_header`      | Header present but not a valid Bearer token |

## Rollback

There is no rollback to Phase 2 short of `git revert` + `docker-compose down -v` + a fresh stack with the old code. By design.

## Validation checklist (kept here for posterity)

- [x] `go build ./...`
- [x] `go vet ./...`
- [x] `go test ./...` (41 tests total: 11 provider + 21 bootstrap + 9 user)
- [x] `docker-compose config` parses cleanly
- [x] `docker-compose up -d` (full stack runs — all 4 containers healthy)
- [x] Keycloak token → `/me` works (see [VALIDATION_PHASE3.md](../VALIDATION_PHASE3.md) §3)
- [x] Second + third `/me` calls return the same local `id` (stable identity verified)
- [x] No duplicate local rows under repeated logins (DB row count = 1 per subject)
- [x] Two distinct subjects yield two distinct local IDs (no collision)
