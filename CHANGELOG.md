# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the project is pre-1.0, minor version bumps may introduce breaking changes;
breaking changes are always called out under a **Breaking** subsection.

## [Unreleased]

## [0.2.0] — 2026-05-20

Identity Management milestone. Adds an admin-only HTTP surface that wraps the
Keycloak Admin API for user, role, session, and invitation administration,
plus role-based access control middleware and a minimal admin web UI.

Full release notes: [docs/RELEASE_v0.2.md](docs/RELEASE_v0.2.md).

### Added

- **Admin API** (`/admin/*`, all `Bearer` + `admin` realm role):
  - `/admin/users` — list, get, update, delete; password-reset email;
    per-user roles (list / assign / remove); per-user sessions (list / revoke-all).
  - `/admin/invitations` — list pending, create (dispatches `VERIFY_EMAIL`
    + `UPDATE_PASSWORD` action emails), revoke, resend.
  - `/admin/roles` — list, create, get, update description, delete; list
    users carrying a role.
  - `/admin/sessions` — list realm-wide active sessions; revoke individual
    session.
- **RBAC middleware**: `auth.RequireRole(role)` and `auth.RequireAnyRole(...)`
  for declarative role gates at the route-group level. Denials emit a
  structured `EventForbidden` `AuthEvent` for observability parity with
  authn failures.
- **Admin web UI** under `web/admin/` (static `index.html` + assets) —
  thin client over the Admin API for local development and ops.
- **`features.identity_management`** flag in `config/project.json` —
  gates mounting of the `/admin/*` group at server startup.
- **New environment variables** (consumed by the service-account client
  that calls the Keycloak Admin API):
  - `KEYCLOAK_ADMIN_CLIENT_ID`
  - `KEYCLOAK_ADMIN_CLIENT_SECRET`
  - `KEYCLOAK_ADMIN_BASE_URL` (defaults to in-network
    `http://keycloak:8080` in `docker-compose`; deliberately distinct from
    `KEYCLOAK_URL` so issuer matching is unaffected).
- **Bootstrap regen** writes the new admin client into
  `deploy/keycloak/realm-export.json` and seeds the new env keys into
  `.env` / `.env.example`.

### Changed

- `docs/swagger.{json,yaml,docs.go}` regenerated to cover the new
  `/admin/*` endpoints. API `info.version` stays at `1.0` (additive,
  non-breaking surface change).
- `internal/server/router.go` adds the `admin` route group with
  `RequireAuth + RequireRole("admin")` applied at the group level.

### Notes

- No data migrations are required; the `users` table schema is unchanged
  from `0.1.0`.
- The Admin API is **dev-only by default** in the same sense as the rest
  of the stack — see [README §Production hardening](README.md#production-hardening)
  before exposing it.

## [0.1.0] — 2026-05-17

Initial tagged release — **Authentication foundation** (`v0.1.0-auth-foundation`).

### Added

- Keycloak-delegated authentication: JWKS-validated RS256 tokens via
  `github.com/MicahParks/keyfunc/v3` with kid-miss refresh.
- `auth.AuthProvider` interface (Keycloak today, provider-agnostic by design).
- `auth.RequireAuth(provider)` Gin middleware; `Identity` propagated via
  `gin.Context`.
- Idempotent just-in-time user provisioning on first protected request
  (`/me`); race-safe via DB unique index on `keycloak_sub`.
- Structured `auth.AuthEvent` + `auth.SetEventHook` for observability.
- Config-as-source-of-truth bootstrap (`config/project.json` →
  `make regen` → `.env`, `realm-export.json`, `project.schema.json`).
- Categorized `make help`; `make doctor` toolchain probe;
  `make reset-dev` one-command rescue.
- Swagger / OpenAPI documentation via `swaggo`; CI gate
  `make swagger-check` blocks drift between handlers and committed specs.
- 41 unit tests, including a 50-goroutine race on JIT user provisioning.

[Unreleased]: https://github.com/joaogabrielvianna/lightweight-saas-backend/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/joaogabrielvianna/lightweight-saas-backend/compare/v0.1.0-auth-foundation...v0.2.0
[0.1.0]: https://github.com/joaogabrielvianna/lightweight-saas-backend/releases/tag/v0.1.0-auth-foundation
